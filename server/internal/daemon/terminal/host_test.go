//go:build linux

package terminal

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder is an OutputSink that keeps what each terminal produced.
type recorder struct {
	mu     sync.Mutex
	chunks map[string]*bytes.Buffer
	woke   chan struct{}
}

func newRecorder() *recorder {
	return &recorder{chunks: map[string]*bytes.Buffer{}, woke: make(chan struct{}, 1)}
}

func (r *recorder) sink(terminalID string, chunk []byte) bool {
	r.mu.Lock()
	if r.chunks[terminalID] == nil {
		r.chunks[terminalID] = &bytes.Buffer{}
	}
	r.chunks[terminalID].Write(chunk)
	r.mu.Unlock()
	select {
	case r.woke <- struct{}{}:
	default:
	}
	return true
}

func (r *recorder) text(terminalID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chunks[terminalID] == nil {
		return ""
	}
	return r.chunks[terminalID].String()
}

func (r *recorder) reset(terminalID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chunks, terminalID)
}

// waitFor fails unless the terminal's output contains want within a few
// seconds.
func (r *recorder) waitFor(t *testing.T, terminalID, want string) {
	t.Helper()
	deadline := time.After(shellDeadline)
	for !strings.Contains(r.text(terminalID), want) {
		select {
		case <-r.woke:
		case <-deadline:
			t.Fatalf("terminal %s never printed %q; got %q", terminalID, want, r.text(terminalID))
		}
	}
}

// staysWithout fails if the terminal's output contains unwanted within d.
func (r *recorder) staysWithout(t *testing.T, terminalID, unwanted string, d time.Duration) {
	t.Helper()
	if strings.Contains(r.text(terminalID), unwanted) {
		t.Fatalf("terminal %s printed %q: %q", terminalID, unwanted, r.text(terminalID))
	}
	time.Sleep(d)
	if strings.Contains(r.text(terminalID), unwanted) {
		t.Fatalf("terminal %s printed %q: %q", terminalID, unwanted, r.text(terminalID))
	}
}

func newTestHost(t *testing.T, root string) (*Host, *recorder) {
	t.Helper()
	rec := newRecorder()
	h := NewHost(Config{Root: root, Shell: "/bin/sh", Logger: quietLogger()}, rec.sink)
	t.Cleanup(h.CloseAll)
	return h, rec
}

func mustOpen(t *testing.T, h *Host, cwd string, cols, rows int) *Session {
	t.Helper()
	s, refusal := h.Open(cwd, cols, rows)
	if refusal != nil {
		t.Fatalf("open %s: %v", cwd, refusal)
	}
	s.Serve()
	return s
}

func nextExit(t *testing.T, h *Host) ExitEvent {
	t.Helper()
	type result struct {
		event ExitEvent
		ok    bool
	}
	ch := make(chan result, 1)
	go func() {
		event, ok := <-h.Exits()
		ch <- result{event, ok}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			t.Fatal("exits channel closed")
		}
		return r.event
	case <-time.After(shellDeadline):
		t.Fatal("no exit was reported")
		return ExitEvent{}
	}
}

func TestHostOpenRefusesWhatTheRootForbids(t *testing.T) {
	root := t.TempDir()
	h, _ := newTestHost(t, root)
	outside := t.TempDir()

	for _, tc := range []struct {
		name, cwd  string
		cols, rows int
		code       string
	}{
		{"outside the root", outside, 80, 24, protocol.TerminalErrPathNotAllowed},
		{"a missing directory", filepath.Join(root, "missing"), 80, 24, protocol.TerminalErrBadRequest},
		{"a relative path", "app", 80, 24, protocol.TerminalErrBadRequest},
		{"zero columns", root, 0, 24, protocol.TerminalErrBadRequest},
		{"too many rows", root, 80, protocol.MaxTerminalRows + 1, protocol.TerminalErrBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if s, refusal := h.Open(tc.cwd, tc.cols, tc.rows); refusal == nil || refusal.Code != tc.code || s != nil {
				t.Fatalf("open = %v, %v; want %s", s, refusal, tc.code)
			}
		})
	}

	unconfigured, _ := newTestHost(t, "")
	if _, refusal := unconfigured.Open(root, 80, 24); refusal == nil || refusal.Code != protocol.TerminalErrTerminalRootUnconfigured ||
		!strings.Contains(refusal.Message, RootEnv) {
		t.Fatalf("open without a root = %v, want %s naming %s", refusal, protocol.TerminalErrTerminalRootUnconfigured, RootEnv)
	}
}

func TestHostStreamsOutputOnlyWhileObserved(t *testing.T) {
	root := t.TempDir()
	h, rec := newTestHost(t, root)
	s := mustOpen(t, h, root, 80, 24)
	if s.PID <= 0 || !processRunning(s.PID) {
		t.Fatalf("session reports pid %d, which is not a running process", s.PID)
	}

	h.Write(s.ID, []byte("echo marker-one\n"))
	rec.waitFor(t, s.ID, "marker-one")

	// Detached: output is read and dropped, so nothing is transported.
	h.Detach(s.ID)
	rec.reset(s.ID)
	h.Write(s.ID, []byte("echo marker-two\n"))
	rec.staysWithout(t, s.ID, "marker-two", 300*time.Millisecond)

	// Attached again: the same shell, still where it was.
	if _, refusal := h.Attach(s.ID); refusal != nil {
		t.Fatalf("attach: %v", refusal)
	}
	h.Write(s.ID, []byte("echo marker-three\n"))
	rec.waitFor(t, s.ID, "marker-three")

	if cols, rows := s.Size(); cols != 80 || rows != 24 {
		t.Fatalf("size = %dx%d, want 80x24", cols, rows)
	}
}

func TestHostResizeReachesTheProgram(t *testing.T) {
	root := t.TempDir()
	h, rec := newTestHost(t, root)
	s := mustOpen(t, h, root, 80, 24)

	h.Write(s.ID, []byte("stty size\n"))
	rec.waitFor(t, s.ID, "24 80")
	h.Resize(s.ID, 100, 30)
	h.Write(s.ID, []byte("stty size\n"))
	rec.waitFor(t, s.ID, "30 100")
	if cols, rows := s.Size(); cols != 100 || rows != 30 {
		t.Fatalf("size = %dx%d, want 100x30", cols, rows)
	}
}

func TestHostReportsHowEachShellEnded(t *testing.T) {
	t.Run("a normal exit keeps its code, after its last output", func(t *testing.T) {
		root := t.TempDir()
		h, rec := newTestHost(t, root)
		s := mustOpen(t, h, root, 80, 24)
		h.Write(s.ID, []byte("echo last-words; exit 4\n"))
		event := nextExit(t, h)
		if event.TerminalID != s.ID || event.Exit.Code == nil || *event.Exit.Code != 4 || event.Exit.Signal != "" {
			t.Fatalf("exit = %+v, want %s with code 4", event, s.ID)
		}
		// The exit is reported only once the shell's last output has been
		// carried, so the server can relay output before exit.
		if !strings.Contains(rec.text(s.ID), "last-words") {
			t.Fatalf("output at exit = %q, want the shell's last words", rec.text(s.ID))
		}
	})
	t.Run("a close reports the signal that ended the shell", func(t *testing.T) {
		root := t.TempDir()
		h, _ := newTestHost(t, root)
		s := mustOpen(t, h, root, 80, 24)
		h.Close(s.ID)
		event := nextExit(t, h)
		if event.TerminalID != s.ID || event.Exit.Code != nil || event.Exit.Signal == "" {
			t.Fatalf("exit = %+v, want %s by a signal", event, s.ID)
		}
		waitForGone(t, s.PID)
		if _, refusal := h.Attach(s.ID); refusal == nil || refusal.Code != protocol.TerminalErrNotFound {
			t.Fatalf("attach to an ended terminal = %v, want %s", refusal, protocol.TerminalErrNotFound)
		}
	})
}

// A program that reads no input fills its terminal's input queue and blocks
// the writer. That must stall its own terminal only.
func TestHostInputForATerminalThatReadsNoneStallsOnlyThatTerminal(t *testing.T) {
	root := t.TempDir()
	h, rec := newTestHost(t, root)
	stuck := mustOpen(t, h, root, 80, 24)
	other := mustOpen(t, h, root, 80, 24)
	h.Write(stuck.ID, []byte("sleep 60\n"))
	rec.waitFor(t, stuck.ID, "sleep 60")

	paste := bytes.Repeat([]byte("x"), 1024)
	for i := 0; i < 200; i++ {
		h.Write(stuck.ID, paste)
	}
	h.Write(other.ID, []byte("echo still-here\n"))
	rec.waitFor(t, other.ID, "still-here")

	// Unknown terminals drop their input without complaint.
	h.Write("no-such-terminal", []byte("ignored"))
}

func TestHostCloseAllEndsEveryTerminal(t *testing.T) {
	root := t.TempDir()
	h, _ := newTestHost(t, root)
	first, second := mustOpen(t, h, root, 80, 24), mustOpen(t, h, root, 80, 24)
	h.CloseAll()
	for _, pid := range []int{first.PID, second.PID} {
		waitForGone(t, pid)
	}
}

// fullScreenApp is a curses program, the kind that takes the whole terminal
// and draws again only when told that the terminal's size changed. Each draw
// shows how many resizes the program has seen, so a draw is visible in the
// output.
const fullScreenApp = `#!@@PYTHON@@
import curses

log = open(@@LOG@@, "w")
resizes = 0

def note(text):
    log.write(text + "\n")
    log.flush()

def main(stdscr):
    global resizes
    rows, cols = stdscr.getmaxyx()
    stdscr.addstr(0, 0, "ready")
    stdscr.refresh()
    note("READY %dx%d" % (cols, rows))
    while True:
        if stdscr.getch() == curses.KEY_RESIZE:
            resizes += 1
            rows, cols = stdscr.getmaxyx()
            note("RESIZE %dx%d %d" % (cols, rows, resizes))
            stdscr.erase()
            stdscr.addstr(0, 0, "redraw %d" % resizes)
            stdscr.refresh()

curses.wrapper(main)
`

// fullScreenPython returns a python3 that has curses, or skips.
func fullScreenPython(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed, so there is no full-screen program to draw")
	}
	if err := exec.Command(python, "-c", "import curses").Run(); err != nil {
		t.Skip("this python3 has no curses module")
	}
	return python
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// waitForLine waits until the file has a line that starts with want, and
// returns the file's lines.
func waitForLine(t *testing.T, path, want string) []string {
	t.Helper()
	deadline := time.Now().Add(shellDeadline)
	for {
		lines := readLines(t, path)
		for _, line := range lines {
			if strings.HasPrefix(line, want) {
				return lines
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never got a line starting with %q; it has %q", path, want, lines)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A socket that attaches has no scrollback, so the program in the terminal
// has to draw its whole screen again. It does so because the attach changes
// the terminal's size by one row and back.
func TestHostAttachMakesAFullScreenProgramDrawAgain(t *testing.T) {
	python := fullScreenPython(t)
	dir := t.TempDir()
	logFile := filepath.Join(dir, "sizes.log")
	program := filepath.Join(dir, "fullscreen")
	script := strings.NewReplacer("@@PYTHON@@", python, "@@LOG@@", strconv.Quote(logFile)).Replace(fullScreenApp)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	rec := newRecorder()
	h := NewHost(Config{Root: root, Shell: program, Logger: quietLogger()}, rec.sink)
	t.Cleanup(h.CloseAll)
	s := mustOpen(t, h, root, 80, 24)
	// READY is noted once the first screen is drawn. Waiting for the screen
	// itself, as the host carried it, leaves nothing of the program's output
	// on its way when the socket attaches: a repaint would take that for the
	// program's reaction to its resize.
	waitForLine(t, logFile, "READY 80x24")
	rec.waitFor(t, s.ID, "ready")

	rec.reset(s.ID)
	h.Detach(s.ID)
	if _, refusal := h.Attach(s.ID); refusal != nil {
		t.Fatalf("attach: %v", refusal)
	}

	// resizes returns the sizes the program was told of so far, in order.
	resizes := func() []string {
		var told []string
		for _, line := range readLines(t, logFile) {
			if strings.HasPrefix(line, "RESIZE ") {
				told = append(told, line)
			}
		}
		return told
	}
	// The repaint is over once the program has drawn for its own size again.
	// Its last line names that size, and counts how many sizes it has seen;
	// its draws carry the same count, so the draw that belongs to the line
	// is the one to wait for.
	var told []string
	deadline := time.Now().Add(shellDeadline)
	for {
		told = resizes()
		last := ""
		if len(told) > 0 {
			last = told[len(told)-1]
		}
		if strings.HasPrefix(last, "RESIZE 80x24 ") &&
			strings.Contains(rec.text(s.ID), "redraw "+strings.TrimPrefix(last, "RESIZE 80x24 ")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the program was told of these sizes: %q and wrote %q", told, rec.text(s.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// It was told of a row less first, and of its own size last: told of
	// one change only, ncurses keeps the screen and updates it in place, and
	// the newly attached side never sees what it missed.
	if len(told) != 2 || !strings.HasPrefix(told[0], "RESIZE 80x23 ") {
		t.Fatalf("the program was told of these sizes: %q; want a row less first and its own size last", told)
	}
	if cols, rows := s.Size(); cols != 80 || rows != 24 {
		t.Fatalf("size after the attach = %dx%d, want 80x24", cols, rows)
	}
}
