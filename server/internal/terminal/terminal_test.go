package terminal

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const readTimeout = 5 * time.Second

// harness is a relay behind a test server that mounts the three production
// routes. The daemon route stands in for middleware.DaemonAuth: a request
// carrying X-Test-Daemon-Token authenticates as that daemon token, which sets
// no X-User-ID, exactly as the real middleware does.
type harness struct {
	t     *testing.T
	trm   *Terminal
	flags *featureflag.StaticProvider
	srv   *httptest.Server
}

func startHarness(t *testing.T, enabled bool, opts ...func(*Options)) *harness {
	t.Helper()
	flags := featureflag.NewStaticProvider()
	if enabled {
		flags.Set(featureflags.TerminalV1, featureflag.Rule{Default: true})
	}
	options := Options{FeatureFlags: featureflag.NewService(flags)}
	for _, opt := range opts {
		opt(&options)
	}
	trm := New(options)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/terminal/ticket", trm.HandleTicket)
	mux.HandleFunc("GET /terminal/ws", trm.HandleBrowserWS)
	mux.HandleFunc("GET /api/daemon/terminal/ws", func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Test-Daemon-Token"); id != "" {
			r.Header.Del("X-User-ID")
			ctx := middleware.WithDaemonContext(r.Context(), "workspace-1", id)
			r = r.WithContext(middleware.WithDaemonTokenOwnerID(ctx, r.Header.Get("X-Test-Token-Owner")))
		}
		trm.HandleDaemonWS(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &harness{t: t, trm: trm, flags: flags, srv: srv}
}

func (h *harness) ticket(userID string) TicketResponse {
	h.t.Helper()
	req := testutil.JSONRequest(http.MethodPost, "/api/terminal/ticket", nil)
	req.Header.Set("X-User-ID", userID)
	var out TicketResponse
	testutil.Call(h.t, h.trm.HandleTicket, req).Want(http.StatusOK).JSON(&out)
	return out
}

func (h *harness) dial(path string, header http.Header) (*websocket.Conn, *http.Response) {
	h.t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(h.srv.URL, "http")+path, header)
	if err == nil {
		h.t.Cleanup(func() { conn.Close() })
	}
	return conn, resp
}

// browser opens a socket with a fresh ticket for userID.
func (h *harness) browser(userID string) *testSocket {
	h.t.Helper()
	conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(h.ticket(userID).Ticket), nil)
	if conn == nil {
		h.t.Fatalf("browser dial: %v", resp.StatusCode)
	}
	return &testSocket{t: h.t, conn: conn}
}

// daemon connects as userID's daemon daemonID, declaring the capability.
func (h *harness) daemon(userID, daemonID string) *testSocket {
	h.t.Helper()
	conn, resp := h.dial("/api/daemon/terminal/ws?daemon_id="+url.QueryEscape(daemonID), http.Header{
		"X-User-Id":             {userID},
		"X-Client-Capabilities": {protocol.DaemonCapabilityTerminalV1},
	})
	if conn == nil {
		h.t.Fatalf("daemon dial: %v", resp.StatusCode)
	}
	// The handshake completes before the relay's side of the connection is
	// up; an open sent meanwhile would find no daemon.
	deadline := time.Now().Add(readTimeout)
	for {
		h.trm.mu.Lock()
		connected := h.trm.daemons[daemonKey{userID: userID, daemonID: daemonID}] != nil
		h.trm.mu.Unlock()
		if connected {
			break
		}
		if time.Now().After(deadline) {
			h.t.Fatal("the relay never took up the daemon connection")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return &testSocket{t: h.t, conn: conn}
}

// testSocket is one end of a terminal WebSocket in a test.
type testSocket struct {
	t    *testing.T
	conn *websocket.Conn
}

func (s *testSocket) send(v any) {
	s.t.Helper()
	s.conn.SetWriteDeadline(time.Now().Add(readTimeout))
	if err := s.conn.WriteJSON(v); err != nil {
		s.t.Fatalf("write %v: %v", v, err)
	}
}

func (s *testSocket) sendBinary(data []byte) {
	s.t.Helper()
	s.conn.SetWriteDeadline(time.Now().Add(readTimeout))
	if err := s.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		s.t.Fatalf("write binary: %v", err)
	}
}

func (s *testSocket) next() (int, []byte) {
	s.t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(readTimeout))
	typ, data, err := s.conn.ReadMessage()
	if err != nil {
		s.t.Fatalf("read: %v", err)
	}
	return typ, data
}

// expect reads the next control message and checks its type.
func (s *testSocket) expect(typ string) protocol.DaemonTerminalMessage {
	s.t.Helper()
	frameTyp, data := s.next()
	if frameTyp != websocket.TextMessage {
		s.t.Fatalf("want a %s text frame, got type %d %q", typ, frameTyp, data)
	}
	var msg protocol.DaemonTerminalMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		s.t.Fatalf("decode %q: %v", data, err)
	}
	if msg.Type != typ {
		s.t.Fatalf("want a %s message, got %q (%s)", typ, msg.Type, data)
	}
	return msg
}

func (s *testSocket) nextBinary() []byte {
	s.t.Helper()
	typ, data := s.next()
	if typ != websocket.BinaryMessage {
		s.t.Fatalf("want a binary frame, got type %d %q", typ, data)
	}
	return data
}

func (s *testSocket) nextBrowser() map[string]any {
	s.t.Helper()
	typ, data := s.next()
	if typ != websocket.TextMessage {
		s.t.Fatalf("want a control frame, got a binary frame %q", data)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		s.t.Fatalf("decode %q: %v", data, err)
	}
	return msg
}

// wantClosed fails unless the next read reports the socket has closed.
func (s *testSocket) wantClosed() {
	s.t.Helper()
	s.conn.SetReadDeadline(time.Now().Add(readTimeout))
	if typ, data, err := s.conn.ReadMessage(); err == nil {
		s.t.Fatalf("socket still open: read type %d %q", typ, data)
	}
}

func TestTicketEndpoint(t *testing.T) {
	t.Run("answers 404 while the flag is off", func(t *testing.T) {
		h := startHarness(t, false)
		req := testutil.JSONRequest(http.MethodPost, "/api/terminal/ticket", nil)
		req.Header.Set("X-User-ID", "user-a")
		testutil.Call(t, h.trm.HandleTicket, req).Want(http.StatusNotFound)
	})
	t.Run("requires an authenticated user", func(t *testing.T) {
		h := startHarness(t, true)
		testutil.Call(t, h.trm.HandleTicket, testutil.JSONRequest(http.MethodPost, "/api/terminal/ticket", nil)).
			Want(http.StatusUnauthorized)
	})
	t.Run("issues an opaque ticket valid for two minutes", func(t *testing.T) {
		h := startHarness(t, true)
		got := h.ticket("user-a")
		if got.Ticket == "" || len(got.Ticket) < 20 || got.ExpiresInSeconds != 120 {
			t.Fatalf("ticket = %+v", got)
		}
		if again := h.ticket("user-a"); again.Ticket == got.Ticket {
			t.Fatal("two issues minted the same ticket")
		}
	})
	t.Run("opens exactly one socket", func(t *testing.T) {
		h := startHarness(t, true)
		ticket := h.ticket("user-a")
		path := "/terminal/ws?ticket=" + url.QueryEscape(ticket.Ticket)
		if conn, resp := h.dial(path, nil); conn == nil {
			t.Fatalf("first dial: %v", resp.StatusCode)
		}
		if conn, resp := h.dial(path, nil); conn != nil {
			conn.Close()
			t.Fatalf("the same ticket opened a second socket")
		} else if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("replay = %v, want 403", resp.StatusCode)
		}
	})
	t.Run("expires", func(t *testing.T) {
		h := startHarness(t, true)
		ticket := h.ticket("user-a")
		h.trm.tickets.mu.Lock()
		h.trm.tickets.now = func() time.Time { return time.Now().Add(defaultTicketTTL) }
		h.trm.tickets.mu.Unlock()
		conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(ticket.Ticket), nil)
		if conn != nil {
			conn.Close()
			t.Fatal("an expired ticket opened a socket")
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expired ticket = %v, want 403", resp.StatusCode)
		}
	})
	t.Run("caps the tickets one user holds", func(t *testing.T) {
		h := startHarness(t, true)
		for i := 0; i < maxTicketsPerUser; i++ {
			h.ticket("user-a")
		}
		req := testutil.JSONRequest(http.MethodPost, "/api/terminal/ticket", nil)
		req.Header.Set("X-User-ID", "user-a")
		testutil.Call(t, h.trm.HandleTicket, req).Want(http.StatusTooManyRequests)
		h.ticket("user-b")
	})
}

func TestBrowserSocketHandshake(t *testing.T) {
	t.Run("refuses with 404 while the flag is off and keeps the ticket", func(t *testing.T) {
		h := startHarness(t, true)
		ticket := h.ticket("user-a")
		h.flags.Set(featureflags.TerminalV1, featureflag.Rule{})
		conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(ticket.Ticket), nil)
		if conn != nil {
			conn.Close()
			t.Fatal("the socket opened while the flag is off")
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("flag off = %v, want 404", resp.StatusCode)
		}
		h.flags.Set(featureflags.TerminalV1, featureflag.Rule{Default: true})
		if conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(ticket.Ticket), nil); conn == nil {
			t.Fatalf("the refused handshake consumed the ticket: %v", resp.StatusCode)
		}
	})
	t.Run("refuses a ticket from another origin", func(t *testing.T) {
		h := startHarness(t, true)
		ticket := h.ticket("user-a")
		conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(ticket.Ticket), http.Header{"Origin": {"https://evil.example"}})
		if conn != nil {
			conn.Close()
			t.Fatal("a cross-origin page opened the socket")
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-origin = %v, want 403", resp.StatusCode)
		}
		// The refused handshake did not burn the ticket.
		h.browser("user-a")
	})
	t.Run("refuses an unknown ticket", func(t *testing.T) {
		h := startHarness(t, true)
		for _, ticket := range []string{"", "not-a-ticket", strings.Repeat("a", maxTicketLen+1)} {
			if conn, resp := h.dial("/terminal/ws?ticket="+url.QueryEscape(ticket), nil); conn != nil {
				conn.Close()
				t.Fatalf("ticket %q opened a socket", ticket)
			} else if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("ticket %q = %v, want 403", ticket, resp.StatusCode)
			}
		}
	})
}

func TestDaemonSocketHandshake(t *testing.T) {
	declares := http.Header{"X-Client-Capabilities": {protocol.DaemonCapabilityTerminalV1}}
	// ask sends the handshake straight to the handler, as DaemonAuth would
	// after it authenticated the request.
	ask := func(h *harness, mutate func(*http.Request)) *testutil.Response {
		req := testutil.JSONRequest(http.MethodGet, "/api/daemon/terminal/ws?daemon_id=daemon-1", nil)
		for k, v := range declares {
			req.Header[k] = v
		}
		if mutate != nil {
			mutate(req)
		}
		return testutil.Call(t, h.trm.HandleDaemonWS, req)
	}
	withToken := func(daemonID, ownerID string) func(*http.Request) {
		return func(r *http.Request) {
			ctx := middleware.WithDaemonContext(r.Context(), "workspace-1", daemonID)
			*r = *r.WithContext(middleware.WithDaemonTokenOwnerID(ctx, ownerID))
		}
	}

	t.Run("answers 404 while the flag is off", func(t *testing.T) {
		ask(startHarness(t, false), nil).Want(http.StatusNotFound)
	})
	t.Run("requires the capability, a daemon id and a user", func(t *testing.T) {
		h := startHarness(t, true)
		ask(h, func(r *http.Request) { r.Header.Del("X-Client-Capabilities") }).Want(http.StatusBadRequest)
		ask(h, func(r *http.Request) { r.URL.RawQuery = "" }).Want(http.StatusBadRequest)
		ask(h, nil).Want(http.StatusUnauthorized)
	})
	t.Run("a daemon token must name its own daemon", func(t *testing.T) {
		// The token is bound to one daemon, so daemon_id=daemon-1 asserted
		// with daemon-2's token is refused.
		ask(startHarness(t, true), withToken("daemon-2", "user-a")).Want(http.StatusForbidden)
	})
	t.Run("a daemon token must be bound to an owner", func(t *testing.T) {
		ask(startHarness(t, true), withToken("daemon-1", "")).Want(http.StatusUnauthorized)
	})
	t.Run("a daemon token's owner owns its terminals", func(t *testing.T) {
		h := startHarness(t, true)
		conn, resp := h.dial("/api/daemon/terminal/ws?daemon_id=daemon-1", http.Header{
			"X-Test-Daemon-Token":   {"daemon-1"},
			"X-Test-Token-Owner":    {"user-a"},
			"X-User-Id":             {"user-b"},
			"X-Client-Capabilities": {protocol.DaemonCapabilityTerminalV1},
		})
		if conn == nil {
			t.Fatalf("daemon dial: %v", resp.StatusCode)
		}
		d := &testSocket{t: t, conn: conn}
		// A daemon token sets no X-User-ID, so user-b is not the owner.
		other := h.browser("user-b")
		other.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "/srv", Cols: 80, Rows: 24, DaemonID: "daemon-1"})
		other.wantError(protocol.TerminalErrDaemonUnavailable)
		openTerminal(t, h.browser("user-a"), d, "daemon-1", "t1")
	})
}

func TestTicketStoreBounds(t *testing.T) {
	s := newTicketStore(time.Now)
	first, err := s.issue("user-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := s.redeem(first); !ok || user != "user-a" {
		t.Fatalf("redeem = %q %v, want user-a", user, ok)
	}
	if _, ok := s.redeem(first); ok {
		t.Fatal("a ticket was redeemed twice")
	}
	for i := 0; i < maxTicketsPerUser; i++ {
		if _, err := s.issue("user-a", time.Minute); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, err := s.issue("user-a", time.Minute); !errors.Is(err, errTooManyTickets) {
		t.Fatalf("issue past the cap = %v, want errTooManyTickets", err)
	}
	if _, err := s.issue("user-b", time.Minute); err != nil {
		t.Fatalf("another user's issue: %v", err)
	}
	// Expiry frees the cap, including the FIFO.
	now := time.Now()
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := s.redeem(first); ok {
		t.Fatal("an unknown ticket was redeemed")
	}
	if len(s.byID) != 0 || len(s.byUser) != 0 || len(s.expiry) != 0 {
		t.Fatalf("expired tickets were kept: %d ids, %d users, %d queued", len(s.byID), len(s.byUser), len(s.expiry))
	}
	if _, err := s.issue("user-a", time.Minute); err != nil {
		t.Fatalf("issue after expiry: %v", err)
	}
}
