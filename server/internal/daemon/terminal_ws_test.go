package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	dterminal "github.com/multica-ai/multica/server/internal/daemon/terminal"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTerminalURL(t *testing.T) {
	for _, tc := range []struct{ base, daemonID, want string }{
		{"http://localhost:8080", "d1", "ws://localhost:8080/api/daemon/terminal/ws?daemon_id=d1"},
		{"https://multica.example.com/prefix/", "a b", "wss://multica.example.com/prefix/api/daemon/terminal/ws?daemon_id=a+b"},
		{"ws://localhost:8080", "d1", "ws://localhost:8080/api/daemon/terminal/ws?daemon_id=d1"},
	} {
		got, err := terminalURL(tc.base, tc.daemonID)
		if err != nil || got != tc.want {
			t.Errorf("terminalURL(%q, %q) = %q, %v; want %q", tc.base, tc.daemonID, got, err, tc.want)
		}
	}
	if _, err := terminalURL("ftp://localhost", "d1"); err == nil {
		t.Fatal("terminalURL accepted ftp://")
	}
}

// fakeTerminalRelay stands in for the server's daemon-side terminal endpoint.
type fakeTerminalRelay struct {
	status       atomic.Int32 // handshake status while nothing is upgraded
	root         string
	mu           sync.Mutex
	dials        int
	capabilities string
	connected    chan struct{} // one value per handshake that got through
	dropped      chan struct{} // one value per connection the daemon let go
}

func newFakeTerminalRelay(root string) *fakeTerminalRelay {
	r := &fakeTerminalRelay{
		root:      root,
		connected: make(chan struct{}, 16),
		dropped:   make(chan struct{}, 16),
	}
	r.status.Store(http.StatusOK)
	return r
}

func (r *fakeTerminalRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.dials++
	r.capabilities = req.Header.Get("X-Client-Capabilities")
	r.mu.Unlock()
	if status := int(r.status.Load()); status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
	if err != nil {
		return
	}
	r.connected <- struct{}{}
	dterminal.ServeWS(req.Context(), conn, dterminal.Config{TaskRoot: r.root, ProjectRoot: r.root, Shell: "/bin/sh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r.dropped <- struct{}{}
}

func (r *fakeTerminalRelay) count() (dials int, capabilities string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dials, r.capabilities
}

func (r *fakeTerminalRelay) await(t *testing.T, what string, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestTerminalLoopFollowsWhatTheServerOffers(t *testing.T) {
	relay := newFakeTerminalRelay(t.TempDir())
	srv := httptest.NewServer(relay)
	t.Cleanup(srv.Close)

	d := &Daemon{
		cfg:          Config{ServerBaseURL: srv.URL, DaemonID: "daemon-1", TaskRoot: t.TempDir(), ProjectRoot: t.TempDir()},
		client:       NewClient(srv.URL),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		terminalWake: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		d.terminalLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})

	// Nothing is dialed until a heartbeat ack offers the capability.
	select {
	case <-relay.connected:
		t.Fatal("the daemon dialed before the server offered the terminal")
	case <-time.After(150 * time.Millisecond):
	}

	d.noteTerminalServer(true)
	relay.await(t, "the first connection", relay.connected)
	dials, capabilities := relay.count()
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
	if !strings.Contains(capabilities, protocol.DaemonCapabilityTerminalV1) {
		t.Fatalf("capabilities = %q, want %s", capabilities, protocol.DaemonCapabilityTerminalV1)
	}

	// A withdrawal ends the connection at once.
	d.noteTerminalServer(false)
	relay.await(t, "the connection to end on a withdrawal", relay.dropped)
	if d.terminalServer.Load() {
		t.Fatal("the daemon still believes the server offers the terminal")
	}

	// A server that lost the endpoint between the ack and the dial.
	relay.status.Store(http.StatusNotFound)
	d.noteTerminalServer(true)
	deadline := time.Now().Add(5 * time.Second)
	for d.terminalServer.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the daemon kept retrying a server without the endpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// It stays idle rather than retrying: the shortest reconnect delay is a second.
	time.Sleep(1500 * time.Millisecond)
	if dials, _ = relay.count(); dials != 2 {
		t.Fatalf("dials = %d after a 404, want 2", dials)
	}

	// The next offer connects again.
	relay.status.Store(http.StatusOK)
	d.noteTerminalServer(true)
	relay.await(t, "the reconnect after a new offer", relay.connected)
}

func TestTerminalServerOffered(t *testing.T) {
	var none *HeartbeatResponse
	if terminalServerOffered(none) || terminalServerOffered(&HeartbeatResponse{}) {
		t.Fatal("an ack without the capability counts as an offer")
	}
	ack := &HeartbeatResponse{ServerCapabilities: []string{protocol.DaemonCapabilityRPCV1, protocol.DaemonCapabilityTerminalV1}}
	if !terminalServerOffered(ack) {
		t.Fatal("an ack with the capability does not count as an offer")
	}
}
