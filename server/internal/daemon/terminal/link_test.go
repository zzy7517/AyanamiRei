//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newTestLink(t *testing.T, root string) *Link {
	t.Helper()
	l := NewLink(newTestConfig(root, t.TempDir(), "/bin/sh"))
	t.Cleanup(l.Close)
	return l
}

type linkFrame struct {
	terminal string
	payload  []byte
	msg      protocol.DaemonTerminalMessage
	isText   bool
}

// nextLinkFrame reads the next frame the link queued, or fails if none comes.
func nextLinkFrame(t *testing.T, l *Link) linkFrame {
	t.Helper()
	deadline := time.Now().Add(shellDeadline)
	for {
		if frame, ok := l.out.TryTake(); ok {
			if frame.Type == websocket.TextMessage {
				var msg protocol.DaemonTerminalMessage
				if err := json.Unmarshal(frame.Data, &msg); err != nil {
					t.Fatalf("decode %q: %v", frame.Data, err)
				}
				return linkFrame{msg: msg, isText: true}
			}
			terminalID, payload, err := protocol.DecodeTerminalBinary(frame.Data)
			if err != nil {
				t.Fatalf("decode %q: %v", frame.Data, err)
			}
			return linkFrame{terminal: terminalID, payload: payload}
		}
		if time.Now().After(deadline) {
			t.Fatal("the link queued no further frame")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// nextControl reads frames until the next control message.
func nextControl(t *testing.T, l *Link) protocol.DaemonTerminalMessage {
	t.Helper()
	for {
		frame := nextLinkFrame(t, l)
		if frame.isText {
			return frame.msg
		}
	}
}

// outputUntil reads the output of terminalID until it contains want, and
// returns everything read on the way.
func outputUntil(t *testing.T, l *Link, terminalID, want string) string {
	t.Helper()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		frame := nextLinkFrame(t, l)
		if frame.isText {
			t.Fatalf("control message %s arrived while waiting for %q", frame.msg.Type, want)
		}
		if frame.terminal != terminalID {
			t.Fatalf("output addressed to %q, want %q", frame.terminal, terminalID)
		}
		out.Write(frame.payload)
	}
	return out.String()
}

// openOnLink asks the link for a shell and returns the ready answer.
func openOnLink(t *testing.T, l *Link, requestID, cwd string) protocol.DaemonTerminalMessage {
	t.Helper()
	l.Handle(&protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgOpen, RequestID: requestID, Cwd: cwd, Cols: 80, Rows: 24,
	})
	answer := nextControl(t, l)
	if answer.Type != protocol.TerminalMsgReady || answer.RequestID != requestID ||
		answer.TerminalID == "" || answer.PID <= 0 || answer.Cols != 80 || answer.Rows != 24 {
		t.Fatalf("open answered with %+v", answer)
	}
	return answer
}

func TestLinkAnswersTheServer(t *testing.T) {
	root := t.TempDir()
	l := newTestLink(t, root)
	ready := openOnLink(t, l, "r1", root)
	id := ready.TerminalID

	// Keyboard input goes in as a binary frame, and the shell's output comes
	// back addressed to the same terminal.
	// The marker cannot occur in the echo of the typed line, so it is the
	// shell's output that is read.
	l.HandleBinary(protocol.EncodeTerminalBinary(id, []byte("printf 'over-%s\\n' the-link\n")))
	outputUntil(t, l, id, "over-the-link")

	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgResize, TerminalID: id, Cols: 100, Rows: 30})
	l.HandleBinary(protocol.EncodeTerminalBinary(id, []byte("stty size\n")))
	outputUntil(t, l, id, "30 100")

	// An attach answers with the terminal as it is now.
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgAttach, RequestID: "r2", TerminalID: id})
	if again := nextControl(t, l); again.Type != protocol.TerminalMsgReady || again.RequestID != "r2" ||
		again.TerminalID != id || again.PID != ready.PID || again.Cols != 100 || again.Rows != 30 {
		t.Fatalf("attach answered with %+v", again)
	}

	for _, tc := range []struct {
		name string
		msg  protocol.DaemonTerminalMessage
		code string
	}{
		{"an open outside the daemon's folders", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgOpen, RequestID: "r3", Cwd: t.TempDir(), Cols: 80, Rows: 24,
		}, protocol.TerminalErrPathNotAllowed},
		{"an attach to a terminal that does not exist", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgAttach, RequestID: "r4", TerminalID: "no-such-terminal",
		}, protocol.TerminalErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l.Handle(&tc.msg)
			answer := nextControl(t, l)
			if answer.Type != protocol.TerminalMsgError || answer.RequestID != tc.msg.RequestID ||
				answer.Code != tc.code || answer.Message == "" {
				t.Fatalf("answered with %+v, want %s under %s", answer, tc.code, tc.msg.RequestID)
			}
		})
	}

	// A close ends the shell, and the link reports how it ended.
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: id})
	exited := nextControl(t, l)
	if exited.Type != protocol.TerminalMsgExit || exited.TerminalID != id ||
		exited.ExitCode != nil || exited.Signal == "" {
		t.Fatalf("close answered with %+v, want an exit by a signal", exited)
	}
	waitForGone(t, ready.PID)
}

// A daemon with neither a task tree root nor a default project folder opens
// nothing the browser names a directory for.
func TestLinkRefusesEveryOpenForAFolderTheDaemonDoesNotOwn(t *testing.T) {
	l := NewLink(newTestConfig("", "", "/bin/sh"))
	t.Cleanup(l.Close)
	cwd := t.TempDir()
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgOpen, RequestID: "r1", Cwd: cwd, Cols: 80, Rows: 24})
	answer := nextControl(t, l)
	if answer.Type != protocol.TerminalMsgError || answer.RequestID != "r1" || answer.Code != protocol.TerminalErrPathNotAllowed {
		t.Fatalf("answered with %+v", answer)
	}
}

// A project's folder is the server's to name: the link does not judge it
// against the daemon's own folders, as it does a directory the browser named.
func TestLinkOpensTheProjectFoldersTheServerNames(t *testing.T) {
	tasks := t.TempDir()
	projects := filepath.Join(t.TempDir(), "ayanami-projects")
	l := NewLink(newTestConfig(tasks, projects, "/bin/sh"))
	t.Cleanup(l.Close)
	folder := t.TempDir()

	l.Handle(&protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgOpen, RequestID: "r1", Cwd: folder, Cols: 80, Rows: 24,
		Project: protocol.DaemonTerminalProjectFolder,
	})
	answer := nextControl(t, l)
	if answer.Type != protocol.TerminalMsgReady || answer.RequestID != "r1" || answer.PID <= 0 {
		t.Fatalf("open in a project's folder answered with %+v", answer)
	}
	l.HandleBinary(protocol.EncodeTerminalBinary(answer.TerminalID, []byte("printf 'in-'; pwd\n")))
	outputUntil(t, l, answer.TerminalID, "in-"+folder)

	// Opening a project's folder does not make the default project folder.
	if _, err := os.Stat(projects); !os.IsNotExist(err) {
		t.Fatalf("a project's folder was opened and the default project folder exists anyway (Stat error %v)", err)
	}

	// The first shell is ended, so that nothing it still writes is read as the
	// second's.
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: answer.TerminalID})
	if exited := nextControl(t, l); exited.Type != protocol.TerminalMsgExit || exited.TerminalID != answer.TerminalID {
		t.Fatalf("after the close the link sent %+v", exited)
	}

	l.Handle(&protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgOpen, RequestID: "r2", Cols: 80, Rows: 24,
		Project: protocol.DaemonTerminalProjectDefault,
	})
	answer = nextControl(t, l)
	if answer.Type != protocol.TerminalMsgReady || answer.RequestID != "r2" || answer.PID <= 0 {
		t.Fatalf("open in the default project folder answered with %+v", answer)
	}
	l.HandleBinary(protocol.EncodeTerminalBinary(answer.TerminalID, []byte("printf 'in-'; pwd\n")))
	outputUntil(t, l, answer.TerminalID, "in-"+projects)
}

func TestLinkRefusesAProjectOpenItCannotPlace(t *testing.T) {
	l := newTestLink(t, t.TempDir())
	for _, tc := range []struct {
		name string
		msg  protocol.DaemonTerminalMessage
		code string
	}{
		{"a project folder without a directory", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgOpen, RequestID: "r1", Cols: 80, Rows: 24, Project: protocol.DaemonTerminalProjectFolder,
		}, protocol.TerminalErrBadRequest},
		{"a project folder that is not on this machine", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgOpen, RequestID: "r2", Cols: 80, Rows: 24,
			Cwd: filepath.Join(t.TempDir(), "moved-away"), Project: protocol.DaemonTerminalProjectFolder,
		}, protocol.TerminalErrBadRequest},
		{"a default project folder in a size the daemon cannot serve", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgOpen, RequestID: "r3", Cols: 0, Rows: 24, Project: protocol.DaemonTerminalProjectDefault,
		}, protocol.TerminalErrBadRequest},
		{"a project the daemon does not know", protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgOpen, RequestID: "r4", Cols: 80, Rows: 24, Project: "elsewhere",
		}, protocol.TerminalErrBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l.Handle(&tc.msg)
			answer := nextControl(t, l)
			if answer.Type != protocol.TerminalMsgError || answer.RequestID != tc.msg.RequestID || answer.Code != tc.code {
				t.Fatalf("answered with %+v, want %s under %s", answer, tc.code, tc.msg.RequestID)
			}
		})
	}
}

func TestLinkCarriesNoOutputOfAnUnobservedTerminal(t *testing.T) {
	root := t.TempDir()
	l := newTestLink(t, root)
	id := openOnLink(t, l, "r1", root).TerminalID

	// While no socket looks at the terminal, its output is not carried.
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.DaemonTerminalMsgDetach, TerminalID: id})
	l.HandleBinary(protocol.EncodeTerminalBinary(id, []byte("printf 'mu%s\\n' ted\n")))
	time.Sleep(300 * time.Millisecond)
	if _, queued := l.out.TryTake(); queued {
		t.Fatal("output of an unobserved terminal was carried")
	}

	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgAttach, RequestID: "r2", TerminalID: id})
	if again := nextControl(t, l); again.Type != protocol.TerminalMsgReady || again.RequestID != "r2" {
		t.Fatalf("attach answered with %+v", again)
	}
	l.HandleBinary(protocol.EncodeTerminalBinary(id, []byte("printf 'au%s\\n' dible\n")))
	outputUntil(t, l, id, "audible\r\n")
}

func TestLinkCloseEndsEveryTerminal(t *testing.T) {
	root := t.TempDir()
	l := newTestLink(t, root)
	first := openOnLink(t, l, "r1", root)
	second := openOnLink(t, l, "r2", root)

	l.Close()
	for _, pid := range []int{first.PID, second.PID} {
		waitForGone(t, pid)
	}

	// A closed link starts nothing more, so it answers nothing.
	for {
		if _, ok := l.out.TryTake(); !ok {
			break
		}
	}
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgOpen, RequestID: "r3", Cwd: root, Cols: 80, Rows: 24})
	time.Sleep(100 * time.Millisecond)
	if frame, ok := l.out.TryTake(); ok {
		t.Fatalf("a closed link queued %q", frame.Data)
	}
}

// startLinkServer serves the daemon's end of a terminal connection behind a
// test server, and returns a connection to it. stop ends the session, as the
// daemon's shutdown would.
func startLinkServer(t *testing.T, root string) (*websocket.Conn, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ServeWS(ctx, conn, newTestConfig(root, t.TempDir(), "/bin/sh"))
	}))
	t.Cleanup(srv.Close)
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		cancel()
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, cancel
}

func nextWSFrame(t *testing.T, conn *websocket.Conn) (int, []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(shellDeadline))
	typ, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, data
}

func nextWSControl(t *testing.T, conn *websocket.Conn) protocol.DaemonTerminalMessage {
	t.Helper()
	for {
		typ, data := nextWSFrame(t, conn)
		if typ != websocket.TextMessage {
			continue
		}
		var msg protocol.DaemonTerminalMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode %q: %v", data, err)
		}
		return msg
	}
}

// untilWSOutput reads frames until the terminal's output contains want.
func untilWSOutput(t *testing.T, conn *websocket.Conn, terminalID, want string) {
	t.Helper()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		typ, data := nextWSFrame(t, conn)
		if typ != websocket.BinaryMessage {
			t.Fatalf("want output of %q, got a text frame %q", terminalID, data)
		}
		id, payload, err := protocol.DecodeTerminalBinary(data)
		if err != nil {
			t.Fatalf("decode %q: %v", data, err)
		}
		if id != terminalID {
			t.Fatalf("output addressed to %q, want %q", id, terminalID)
		}
		out.Write(payload)
	}
}

func TestServeWSSpeaksTheConnection(t *testing.T) {
	root := t.TempDir()
	conn, stop := startLinkServer(t, root)
	send := func(v any) {
		t.Helper()
		conn.SetWriteDeadline(time.Now().Add(shellDeadline))
		if err := conn.WriteJSON(v); err != nil {
			t.Fatalf("write %v: %v", v, err)
		}
	}
	open := func(requestID string) protocol.DaemonTerminalMessage {
		t.Helper()
		send(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgOpen, RequestID: requestID, Cwd: root, Cols: 80, Rows: 24})
		answer := nextWSControl(t, conn)
		if answer.Type != protocol.TerminalMsgReady || answer.RequestID != requestID ||
			answer.TerminalID == "" || answer.PID <= 0 {
			t.Fatalf("open answered with %+v", answer)
		}
		return answer
	}

	ready := open("r1")
	id := ready.TerminalID
	conn.SetWriteDeadline(time.Now().Add(shellDeadline))
	if err := conn.WriteMessage(websocket.BinaryMessage, protocol.EncodeTerminalBinary(id, []byte("printf 'over-%s\\n' websockets\n"))); err != nil {
		t.Fatal(err)
	}
	untilWSOutput(t, conn, id, "over-websockets\r\n")

	send(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgResize, TerminalID: id, Cols: 100, Rows: 30})
	conn.SetWriteDeadline(time.Now().Add(shellDeadline))
	if err := conn.WriteMessage(websocket.BinaryMessage, protocol.EncodeTerminalBinary(id, []byte("stty size\n"))); err != nil {
		t.Fatal(err)
	}
	untilWSOutput(t, conn, id, "30 100\r\n")

	send(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: id})
	exited := nextWSControl(t, conn)
	if exited.Type != protocol.TerminalMsgExit || exited.TerminalID != id || exited.Signal == "" {
		t.Fatalf("close answered with %+v", exited)
	}
	waitForGone(t, ready.PID)

	// The connection going away ends what was opened over it.
	second := open("r2")
	stop()
	waitForGone(t, second.PID)
}
