package terminal

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/wsoutbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func openMsg(daemonID string) protocol.BrowserTerminalMessage {
	return protocol.BrowserTerminalMessage{
		Type: protocol.TerminalMsgOpen, Cwd: "/srv/app", Cols: 120, Rows: 40, DaemonID: daemonID,
	}
}

// openTerminal opens a shell from b on d and waits until both ends report it.
func openTerminal(t *testing.T, b *testSocket, d *testSocket, daemonID, terminalID string) {
	t.Helper()
	b.send(openMsg(daemonID))
	req := d.expect(protocol.TerminalMsgOpen)
	if req.Cwd != "/srv/app" || req.Cols != 120 || req.Rows != 40 || req.RequestID == "" {
		t.Fatalf("daemon was asked %+v", req)
	}
	d.send(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: terminalID, PID: 4242, Cols: 120, Rows: 40,
	})
	wantControl(t, b.nextBrowser(), map[string]any{
		"type": protocol.TerminalMsgReady, "terminalId": terminalID, "pid": 4242, "cols": 120, "rows": 40,
	})
}

// attach asks b to join terminalID and has the daemon confirm it.
func attach(t *testing.T, b *testSocket, d *testSocket, terminalID string) {
	t.Helper()
	b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: terminalID})
	req := d.expect(protocol.TerminalMsgAttach)
	if req.TerminalID != terminalID || req.RequestID == "" {
		t.Fatalf("daemon was asked %+v", req)
	}
	d.send(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: terminalID, PID: 4242, Cols: 80, Rows: 24,
	})
	wantControl(t, b.nextBrowser(), map[string]any{"type": protocol.TerminalMsgReady, "terminalId": terminalID, "pid": 4242})
}

// wantControl fails unless got carries every key and value of want.
func wantControl(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if n, ok := v.(int); ok {
			v = float64(n)
		}
		if got[k] != v {
			t.Fatalf("message %v: %v = %v, want %v", got, k, got[k], v)
		}
	}
}

// wantError reads the error that ends the socket and waits for the close.
func (s *testSocket) wantError(code string) map[string]any {
	s.t.Helper()
	got := s.nextBrowser()
	if got["type"] != protocol.TerminalMsgError || got["code"] != code {
		s.t.Fatalf("want an error %s, got %v", code, got)
	}
	s.wantClosed()
	return got
}

// nextDaemonBinary reads the next frame the daemon receives and requires it
// to be addressed terminal input.
func (s *testSocket) nextDaemonBinary() (string, []byte) {
	s.t.Helper()
	typ, data := s.next()
	if typ != websocket.BinaryMessage {
		s.t.Fatalf("want terminal input, got type %d %q", typ, data)
	}
	id, payload, err := protocol.DecodeTerminalBinary(data)
	if err != nil {
		s.t.Fatalf("decode %q: %v", data, err)
	}
	return id, payload
}

func (s *testSocket) sendRaw(data string) {
	s.t.Helper()
	s.conn.SetWriteDeadline(time.Now().Add(readTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, []byte(data)); err != nil {
		s.t.Fatalf("write %q: %v", data, err)
	}
}

func (s *testSocket) wantOutput(want string) {
	s.t.Helper()
	if got := s.nextBinary(); string(got) != want {
		s.t.Fatalf("browser output = %q, want %q", got, want)
	}
}

func TestRelayCarriesAStreamBetweenBrowserAndDaemon(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	b := h.browser("user-a")
	openTerminal(t, b, d, "daemon-a", "t1")

	b.sendBinary([]byte("echo hello\n"))
	if id, input := d.nextDaemonBinary(); id != "t1" || string(input) != "echo hello\n" {
		t.Fatalf("daemon got input for %q: %q", id, input)
	}
	d.sendBinary(protocol.EncodeTerminalBinary("t1", []byte("hello\r\n\x1b[?2004h")))
	b.wantOutput("hello\r\n\x1b[?2004h")

	b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgResize, Cols: 80, Rows: 25})
	if got := d.expect(protocol.TerminalMsgResize); got.TerminalID != "t1" || got.Cols != 80 || got.Rows != 25 {
		t.Fatalf("daemon was asked %+v", got)
	}

	zero := 0
	d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgExit, TerminalID: "t1", ExitCode: &zero})
	exit := b.nextBrowser()
	wantControl(t, exit, map[string]any{"type": protocol.TerminalMsgExit, "terminalId": "t1", "exitCode": 0})
	b.wantClosed()
}

func TestOpenNeedsAnEligibleDaemon(t *testing.T) {
	t.Run("no daemon connected", func(t *testing.T) {
		h := startHarness(t, true)
		b := h.browser("user-a")
		b.send(openMsg(""))
		b.wantError(protocol.TerminalErrDaemonUnavailable)
	})
	t.Run("only another user's daemon", func(t *testing.T) {
		h := startHarness(t, true)
		h.daemon("user-b", "daemon-b")
		for _, daemonID := range []string{"", "daemon-b"} {
			b := h.browser("user-a")
			b.send(openMsg(daemonID))
			b.wantError(protocol.TerminalErrDaemonUnavailable)
		}
	})
	t.Run("a choice is needed between two daemons", func(t *testing.T) {
		h := startHarness(t, true)
		h.daemon("user-a", "daemon-1")
		d2 := h.daemon("user-a", "daemon-2")
		b := h.browser("user-a")
		b.send(openMsg(""))
		b.wantError(protocol.TerminalErrDaemonUnavailable)
		openTerminal(t, h.browser("user-a"), d2, "daemon-2", "t1")
	})
	t.Run("the named daemon is not connected", func(t *testing.T) {
		h := startHarness(t, true)
		h.daemon("user-a", "daemon-1")
		b := h.browser("user-a")
		b.send(openMsg("daemon-9"))
		b.wantError(protocol.TerminalErrDaemonUnavailable)
	})
}

func TestAttachSharesOneTerminal(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	b1 := h.browser("user-a")
	openTerminal(t, b1, d, "daemon-a", "t1")

	b2 := h.browser("user-a")
	attach(t, b2, d, "t1")
	d.sendBinary(protocol.EncodeTerminalBinary("t1", []byte("both\r\n")))
	b1.wantOutput("both\r\n")
	b2.wantOutput("both\r\n")

	// One of two readers leaving changes nothing at the daemon.
	b1.conn.Close()
	d.sendBinary(protocol.EncodeTerminalBinary("t1", []byte("still\r\n")))
	b2.wantOutput("still\r\n")

	// The last one leaving stops the output; the terminal itself stays.
	b2.conn.Close()
	if got := d.expect(protocol.DaemonTerminalMsgDetach); got.TerminalID != "t1" {
		t.Fatalf("daemon was asked %+v", got)
	}
	attach(t, h.browser("user-a"), d, "t1")

	other := h.browser("user-b")
	other.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
	other.wantError(protocol.TerminalErrNotFound)
}

func TestCloseEndsTheTerminalWithTheDaemonsVerdict(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	b1 := h.browser("user-a")
	openTerminal(t, b1, d, "daemon-a", "t1")

	// The socket that asks may be a different one, not attached to anything.
	asker := h.browser("user-a")
	asker.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: "t1"})
	if got := d.expect(protocol.TerminalMsgClose); got.TerminalID != "t1" {
		t.Fatalf("daemon was asked %+v", got)
	}
	d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgExit, TerminalID: "t1", Signal: "SIGKILL"})
	exit := b1.nextBrowser()
	wantControl(t, exit, map[string]any{"type": protocol.TerminalMsgExit, "terminalId": "t1", "signal": "SIGKILL"})
	if code, present := exit["exitCode"]; !present || code != nil {
		t.Fatalf("exit by a signal reported exitCode = %v (present %v), want an explicit null", code, present)
	}
	b1.wantClosed()

	late := h.browser("user-a")
	late.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
	late.wantError(protocol.TerminalErrNotFound)

	// Closing what is gone, or another user's, is not an error and reaches no
	// daemon: the next thing this daemon is asked is the open that follows.
	stranger := h.browser("user-b")
	stranger.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: "t1"})
	openTerminal(t, h.browser("user-a"), d, "daemon-a", "t2")
}

func TestDaemonRefusalsReachTheBrowser(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
	}{
		{"root unconfigured", protocol.TerminalErrTerminalRootUnconfigured, protocol.TerminalErrTerminalRootUnconfigured},
		{"outside the root", protocol.TerminalErrPathNotAllowed, protocol.TerminalErrPathNotAllowed},
		{"unknown terminal", protocol.TerminalErrNotFound, protocol.TerminalErrNotFound},
		{"unsupported platform", protocol.TerminalErrUnsupportedPlatform, protocol.TerminalErrUnsupportedPlatform},
		{"bad request", protocol.TerminalErrBadRequest, protocol.TerminalErrBadRequest},
		{"a code outside the contract", "daemon:exploded", protocol.TerminalErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startHarness(t, true)
			d := h.daemon("user-a", "daemon-a")
			b := h.browser("user-a")
			b.send(openMsg("daemon-a"))
			req := d.expect(protocol.TerminalMsgOpen)
			d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgError, RequestID: req.RequestID, Code: tc.code, Message: "why"})
			if got := b.wantError(tc.want); got["message"] != "why" {
				t.Fatalf("error = %v, want the daemon's message", got)
			}
		})
	}

	t.Run("a refused attach ends the terminal for everyone on it", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("user-a", "daemon-a")
		b1 := h.browser("user-a")
		openTerminal(t, b1, d, "daemon-a", "t1")
		b2 := h.browser("user-a")
		b2.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
		req := d.expect(protocol.TerminalMsgAttach)
		d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgError, RequestID: req.RequestID, Code: protocol.TerminalErrNotFound})
		b2.wantError(protocol.TerminalErrNotFound)
		b1.wantError(protocol.TerminalErrNotFound)
	})
}

func TestDaemonDisconnectEndsItsTerminals(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	b := h.browser("user-a")
	openTerminal(t, b, d, "daemon-a", "t1")

	joining := h.browser("user-a")
	joining.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
	d.expect(protocol.TerminalMsgAttach)

	d.conn.Close()
	b.wantError(protocol.TerminalErrDaemonUnavailable)
	joining.wantError(protocol.TerminalErrDaemonUnavailable)

	late := h.browser("user-a")
	late.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
	late.wantError(protocol.TerminalErrNotFound)
	reopening := h.browser("user-a")
	reopening.send(openMsg("daemon-a"))
	reopening.wantError(protocol.TerminalErrDaemonUnavailable)
}

func TestDaemonReconnectReplacesItsConnection(t *testing.T) {
	h := startHarness(t, true)
	d1 := h.daemon("user-a", "daemon-a")
	b := h.browser("user-a")
	openTerminal(t, b, d1, "daemon-a", "t1")

	d2 := h.daemon("user-a", "daemon-a")
	d1.wantClosed()
	b.wantError(protocol.TerminalErrDaemonUnavailable)
	openTerminal(t, h.browser("user-a"), d2, "daemon-a", "t2")
}

func TestUnansweredRequestTimesOut(t *testing.T) {
	h := startHarness(t, true, func(o *Options) { o.OpenTimeout = 150 * time.Millisecond })
	d := h.daemon("user-a", "daemon-a")

	b := h.browser("user-a")
	b.send(openMsg("daemon-a"))
	req := d.expect(protocol.TerminalMsgOpen)
	b.wantError(protocol.TerminalErrDaemonUnavailable)
	// A late answer is undone rather than left running unattended.
	d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t1", PID: 7, Cols: 80, Rows: 24})
	if got := d.expect(protocol.TerminalMsgClose); got.TerminalID != "t1" {
		t.Fatalf("daemon was asked %+v", got)
	}

	b1 := h.browser("user-a")
	openTerminal(t, b1, d, "daemon-a", "t2")
	b2 := h.browser("user-a")
	b2.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t2"})
	req = d.expect(protocol.TerminalMsgAttach)
	b2.wantError(protocol.TerminalErrDaemonUnavailable)
	d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t2", PID: 7, Cols: 80, Rows: 24})
	// The socket that opened the terminal is still on it, so the daemon is
	// told nothing: the terminal still carries that socket's input.
	b1.sendBinary([]byte("echo still-attached\n"))
	if id, _ := d.nextDaemonBinary(); id != "t2" {
		t.Fatalf("input went to terminal %q, want t2", id)
	}
}

// A browser that leaves before the daemon answers undoes the request,
// whether the relay notices the leaving before or after the answer comes.
func TestBrowserLeavingBeforeTheAnswerUndoesTheRequest(t *testing.T) {
	t.Run("an open nobody waits for is closed", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("user-a", "daemon-a")
		b := h.browser("user-a")
		b.send(openMsg("daemon-a"))
		req := d.expect(protocol.TerminalMsgOpen)
		b.conn.Close()
		d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t1", PID: 7, Cols: 80, Rows: 24})
		if got := d.expect(protocol.TerminalMsgClose); got.TerminalID != "t1" {
			t.Fatalf("an open nobody waits for was %+v, want close", got)
		}
	})

	t.Run("an attach leaves the terminal to those still on it", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("user-a", "daemon-a")
		b1 := h.browser("user-a")
		openTerminal(t, b1, d, "daemon-a", "t2")
		b2 := h.browser("user-a")
		b2.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t2"})
		req := d.expect(protocol.TerminalMsgAttach)
		b2.conn.Close()
		d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t2", PID: 7, Cols: 80, Rows: 24})

		// The terminal is neither muted nor closed for the socket that
		// opened it.
		b1.sendBinary([]byte("echo still-attached\n"))
		if id, _ := d.nextDaemonBinary(); id != "t2" {
			t.Fatalf("input went to terminal %q, want t2", id)
		}
		d.sendBinary(protocol.EncodeTerminalBinary("t2", []byte("still-attached\r\n")))
		b1.wantOutput("still-attached\r\n")
	})

	t.Run("an attach nobody waits for is undone", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("user-a", "daemon-a")
		b1 := h.browser("user-a")
		openTerminal(t, b1, d, "daemon-a", "t2")
		// A shell whose output was seen is detached, not closed.
		d.sendBinary(protocol.EncodeTerminalBinary("t2", []byte("some output\r\n")))
		b1.wantOutput("some output\r\n")
		b1.conn.Close()
		if got := d.expect(protocol.DaemonTerminalMsgDetach); got.TerminalID != "t2" {
			t.Fatalf("the last socket leaving was %+v, want detach", got)
		}

		b2 := h.browser("user-a")
		b2.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t2"})
		req := d.expect(protocol.TerminalMsgAttach)
		b2.conn.Close()
		d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t2", PID: 7, Cols: 80, Rows: 24})
		if got := d.expect(protocol.DaemonTerminalMsgDetach); got.TerminalID != "t2" {
			t.Fatalf("an attach nobody waits for was %+v, want detach", got)
		}
	})
}

// The answer can also be bound before the relay notices the browser is gone.
// A shell nobody has seen anything of is closed, not detached: nobody knows
// its id, so it could never be attached to.
func TestLastSocketLeavingAShellNobodySawClosesIt(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	openTerminal(t, h.browser("user-a"), d, "daemon-a", "t1")

	leave := func(terminalID string) {
		h.trm.mu.Lock()
		defer h.trm.mu.Unlock()
		target := h.trm.terminals[terminalID]
		if target == nil || len(target.attached) != 1 {
			t.Fatalf("terminal %s = %v with %d sockets, want one socket", terminalID, target, len(target.attached))
		}
		// What the relay does when that socket's reader ends.
		for socket := range target.attached {
			h.trm.browserLeftLocked(socket)
		}
	}
	leave("t1")
	if got := d.expect(protocol.TerminalMsgClose); got.TerminalID != "t1" {
		t.Fatalf("daemon was asked %+v, want close", got)
	}

	// Once output has reached a socket, the shell outlives its last socket.
	openTerminal(t, h.browser("user-a"), d, "daemon-a", "t2")
	d.sendBinary(protocol.EncodeTerminalBinary("t2", []byte("$ ")))
	deadline := time.Now().Add(readTimeout)
	for {
		h.trm.mu.Lock()
		seen := h.trm.terminals["t2"].outputSeen
		h.trm.mu.Unlock()
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the relay never forwarded the terminal's output")
		}
		time.Sleep(5 * time.Millisecond)
	}
	leave("t2")
	if got := d.expect(protocol.DaemonTerminalMsgDetach); got.TerminalID != "t2" {
		t.Fatalf("daemon was asked %+v, want detach", got)
	}
}

func TestInvalidControlMessages(t *testing.T) {
	cases := []struct {
		name string
		send func(b *testSocket)
	}{
		{"malformed JSON", func(b *testSocket) { b.sendRaw(`{not json`) }},
		{"not an object", func(b *testSocket) { b.sendRaw(`[1]`) }},
		{"an unknown type", func(b *testSocket) { b.send(protocol.BrowserTerminalMessage{Type: "ping"}) }},
		{"a relative cwd", func(b *testSocket) {
			b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "srv/app", Cols: 80, Rows: 24})
		}},
		{"a cwd with a trailing slash", func(b *testSocket) {
			b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "/srv/app/", Cols: 80, Rows: 24})
		}},
		{"a cwd with dot-dot", func(b *testSocket) {
			b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "/srv/../etc", Cols: 80, Rows: 24})
		}},
		{"zero columns", func(b *testSocket) {
			b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "/srv", Cols: 0, Rows: 24})
		}},
		{"rows past the maximum", func(b *testSocket) {
			b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgOpen, Cwd: "/srv", Cols: 80, Rows: protocol.MaxTerminalRows + 1})
		}},
		{"an attach without a terminal", func(b *testSocket) { b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startHarness(t, true)
			h.daemon("user-a", "daemon-a")
			b := h.browser("user-a")
			tc.send(b)
			b.wantError(protocol.TerminalErrBadRequest)
		})
	}

	t.Run("a second binding of one socket", func(t *testing.T) {
		for _, send := range []func(*testSocket){
			func(b *testSocket) { b.send(openMsg("daemon-a")) },
			func(b *testSocket) {
				b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t1"})
			},
			func(b *testSocket) { b.send(projectOpenMsg(protocol.TerminalProjectDefault, "daemon-a")) },
			func(b *testSocket) { b.send(projectOpenMsg("folder-1", "daemon-a")) },
		} {
			h := startHarness(t, true, func(o *Options) {
				o.Project = &fakeProjectResolver{launch: &ProjectLaunch{DaemonID: "daemon-a", Cwd: "/srv/app"}}
			})
			d := h.daemon("user-a", "daemon-a")
			b := h.browser("user-a")
			openTerminal(t, b, d, "daemon-a", "t1")
			send(b)
			b.wantError(protocol.TerminalErrBadRequest)
		}
	})
	t.Run("a resize without a valid size", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("user-a", "daemon-a")
		b := h.browser("user-a")
		openTerminal(t, b, d, "daemon-a", "t1")
		b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgResize, Cols: -1, Rows: 24})
		b.wantError(protocol.TerminalErrBadRequest)
	})
}

func TestInputAndResizeBeforeReadyAreIgnored(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	b := h.browser("user-a")
	b.sendBinary([]byte("typed too early\n"))
	b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgResize, Cols: 10, Rows: 10})

	// The open is the first thing the daemon hears.
	b.send(openMsg("daemon-a"))
	req := d.expect(protocol.TerminalMsgOpen)
	if req.Cols != 120 || req.Rows != 40 {
		t.Fatalf("an early resize changed the open: %+v", req)
	}
	d.send(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t1", PID: 7, Cols: 120, Rows: 40})
	b.nextBrowser()

	b.sendBinary([]byte("typed later\n"))
	if id, input := d.nextDaemonBinary(); id != "t1" || string(input) != "typed later\n" {
		t.Fatalf("daemon got input for %q: %q, want only the input sent after ready", id, input)
	}
}

func TestOutputForAnUnknownTerminalIsDropped(t *testing.T) {
	h := startHarness(t, true)
	d := h.daemon("user-a", "daemon-a")
	d.sendBinary(protocol.EncodeTerminalBinary("nope", []byte("output")))
	d.sendRaw(`{"type":"nonsense"}`)
	d.sendRaw(`{not json`)
	openTerminal(t, h.browser("user-a"), d, "daemon-a", "t1")
}

func TestASocketThatCannotKeepUpIsDetached(t *testing.T) {
	trm := New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	b := &browserConn{relay: trm, userID: "user-a", out: wsoutbox.New(4, 2, 1<<20), done: make(chan struct{})}
	d := &daemonConn{
		relay: trm, Identity: DaemonIdentity{UserID: "user-a", DaemonID: "daemon-a"},
		out: wsoutbox.New(8, 4, 1<<20), done: make(chan struct{}), terminals: map[string]*terminal{},
	}
	target := &terminal{ID: "t1", daemon: d, attached: map[*browserConn]struct{}{b: {}}}
	b.terminal = target
	d.terminals["t1"] = target
	trm.terminals["t1"] = target

	chunk := []byte("0123456789")
	trm.mu.Lock()
	trm.outputLocked(d, "t1", chunk)
	trm.outputLocked(d, "t1", chunk)
	if b.closed {
		trm.mu.Unlock()
		t.Fatal("detached while its queue still had room")
	}
	trm.outputLocked(d, "t1", chunk)
	trm.mu.Unlock()
	if !b.closed || b.terminal != nil {
		t.Fatal("a socket whose queue is full was left attached")
	}

	// It keeps what fit, then the error that ends it; the daemon is told the
	// terminal has no reader, and later output is not queued again.
	if got := b.out.Take(); got.Type != websocket.BinaryMessage || string(got.Data) != string(chunk) {
		t.Fatalf("frame = %+v", got)
	}
	if got := b.out.Take(); got.Type != websocket.BinaryMessage || string(got.Data) != string(chunk) {
		t.Fatalf("frame = %+v", got)
	}
	last, ok := b.out.TryTake()
	if !ok || last.Type != websocket.TextMessage || !strings.Contains(string(last.Data), `"code":"internal"`) {
		t.Fatalf("ending frame = %v %q", ok, last.Data)
	}
	if got := d.out.Take(); !strings.Contains(string(got.Data), `"type":"detach"`) {
		t.Fatalf("daemon was asked %q, want detach", got.Data)
	}
	trm.mu.Lock()
	trm.outputLocked(d, "t1", chunk)
	trm.mu.Unlock()
	if _, queued := b.out.TryTake(); queued {
		t.Fatal("output was queued for a detached socket")
	}
}
