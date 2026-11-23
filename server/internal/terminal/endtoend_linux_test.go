//go:build linux

package terminal

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	dterminal "github.com/multica-ai/multica/server/internal/daemon/terminal"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// untilOutput reads the socket's output until it contains want, and returns
// everything read on the way.
func (s *testSocket) untilOutput(want string) string {
	s.t.Helper()
	var out strings.Builder
	for !strings.Contains(out.String(), want) {
		out.Write(s.nextBinary())
	}
	return out.String()
}

// nextControl reads past terminal output to the next control message.
func (s *testSocket) nextControl() map[string]any {
	s.t.Helper()
	for {
		typ, data := s.next()
		if typ != websocket.TextMessage {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			s.t.Fatalf("decode %q: %v", data, err)
		}
		return msg
	}
}

// processGone waits until pid is no longer running.
func processGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(readTimeout)
	for {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// realDaemon connects a daemon and runs the daemon's real terminal side on
// the connection: the PTY host behind the relay, with real shells.
func (h *harness) realDaemon(t *testing.T, userID, daemonID, root string) (*testSocket, <-chan struct{}) {
	h.t.Helper()
	d := h.daemon(userID, daemonID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		dterminal.ServeWS(context.Background(), d.conn, dterminal.Config{
			Root: root, Shell: "/bin/sh", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	t.Cleanup(func() {
		d.conn.Close()
		select {
		case <-done:
		case <-time.After(readTimeout):
			t.Error("the daemon's terminal session did not end with its connection")
		}
	})
	return d, done
}

func openShell(t *testing.T, b *testSocket, daemonID, root string) (string, int) {
	t.Helper()
	b.send(protocol.BrowserTerminalMessage{
		Type: protocol.TerminalMsgOpen, Cwd: root, Cols: 120, Rows: 40, DaemonID: daemonID,
	})
	ready := b.nextControl()
	wantControl(t, ready, map[string]any{"type": protocol.TerminalMsgReady, "cols": 120, "rows": 40})
	terminalID, _ := ready["terminalId"].(string)
	pid := int(ready["pid"].(float64))
	if terminalID == "" || pid <= 0 {
		t.Fatalf("ready = %v", ready)
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
		t.Fatalf("ready named process %d, which is not running here: %v", pid, err)
	}
	return terminalID, pid
}

func TestBrowserRunsAShellOnTheDaemon(t *testing.T) {
	h := startHarness(t, true)
	root := t.TempDir()
	h.realDaemon(t, "user-a", "daemon-a", root)

	b := h.browser("user-a")
	terminalID, pid := openShell(t, b, "daemon-a", root)

	// Keyboard input reaches the shell, and its answer comes back as output.
	b.sendBinary([]byte("printf 'hel%s\\n' lo\n"))
	b.untilOutput("hello\r\n")

	// A resize reaches the program in the terminal.
	b.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgResize, Cols: 100, Rows: 30})
	b.sendBinary([]byte("stty size\n"))
	b.untilOutput("30 100")

	// A second socket joins the same shell and sees what it draws from now on.
	second := h.browser("user-a")
	second.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: terminalID})
	wantControl(t, second.nextControl(), map[string]any{
		"type": protocol.TerminalMsgReady, "terminalId": terminalID, "pid": pid, "cols": 100, "rows": 30,
	})
	b.sendBinary([]byte("printf 'sh%s\\n' ared\n"))
	b.untilOutput("shared\r\n")
	second.untilOutput("shared\r\n")

	// The shell outlives the socket that opened it.
	b.conn.Close()
	second.sendBinary([]byte("printf 'al%s\\n' one\n"))
	second.untilOutput("alone\r\n")

	// Closing it ends the shell, and the socket that asked is told how.
	second.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: terminalID})
	exit := second.nextControl()
	wantControl(t, exit, map[string]any{"type": protocol.TerminalMsgExit, "terminalId": terminalID})
	if exit["signal"] == "" {
		t.Fatalf("exit = %v, want the signal that ended the shell", exit)
	}
	second.wantClosed()
	processGone(t, pid)
}

func TestDaemonDisconnectEndsTheShellsItRuns(t *testing.T) {
	h := startHarness(t, true)
	root := t.TempDir()
	d, _ := h.realDaemon(t, "user-a", "daemon-a", root)

	b := h.browser("user-a")
	_, pid := openShell(t, b, "daemon-a", root)
	b.sendBinary([]byte("printf 'be%s\\n' fore\n"))
	b.untilOutput("before\r\n")

	// The daemon's connection is what keeps its terminals; with it gone they
	// are gone too, and the socket is told rather than left waiting.
	d.conn.Close()
	wantControl(t, b.nextControl(), map[string]any{"type": protocol.TerminalMsgError, "code": protocol.TerminalErrDaemonUnavailable})
	b.wantClosed()
	processGone(t, pid)
}
