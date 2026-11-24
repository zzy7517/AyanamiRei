// Package terminal is the daemon side of the web terminal (terminal_v1): it
// runs the shells, on this machine, that a browser shows.
//
// The daemon owns every pseudo-terminal, so a shell outlives the browser
// sockets looking at it and ends only when it exits, when the server asks to
// close it, or when the daemon's terminal connection to the server ends. A
// Host is the set of shells opened over one such connection; a Link speaks
// the connection's protocol and ServeWS binds it to the WebSocket.
//
// A shell starts in one of three places: a directory the browser named, which
// has to be inside this daemon's task trees or its default project folder; a
// project's registered folder, which the server looked up and may be anywhere
// on this machine; or the default project folder itself. Which of the three an
// open is, this side decides: the server relays a request, it does not decide.
package terminal

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxControlFrame = 16 << 10

	// inputQueue bounds the keyboard input waiting for one terminal. A
	// program that reads none makes its writer block on the terminal; input
	// past the queue is dropped rather than held.
	inputQueue = 64

	// closeGrace is how long a shell has to end on SIGTERM before it is
	// killed.
	closeGrace = 2 * time.Second

	// outputDrainGrace bounds the wait for a shell's last output once it has
	// exited.
	outputDrainGrace = 200 * time.Millisecond

	// repaintReaction bounds the wait for a program's reaction to a resize.
	// A program that ignores its size holds the shorter terminal for this
	// long, which it does not notice.
	repaintReaction = time.Second

	// repaintHold bounds the wait for what a program wrote to be read and
	// to stop, so that a program writing without end holds up no repaint.
	repaintHold = 250 * time.Millisecond

	// repaintSettle is how long a program's output must have stopped for a
	// repaint to take it as ended: what it draws comes in bursts.
	repaintSettle = 20 * time.Millisecond

	// exitQueue bounds the exits waiting to be reported to the server.
	exitQueue = 256
)

// Config configures a Host.
type Config struct {
	// TaskRoot is where this daemon's task trees live. A shell the browser
	// names a directory for may start in one of them.
	TaskRoot string
	// ProjectRoot is the default project folder: a shell may start in it, and
	// it is created when a terminal is first opened there.
	ProjectRoot string
	// Shell is the program a terminal runs. Empty picks DefaultShell.
	Shell string
	// Logger receives the host's warnings; empty means slog.Default.
	Logger *slog.Logger
	// Launcher starts agent sessions (an open with an agent part). Nil
	// refuses them: this daemon then hosts shells only.
	Launcher Launcher
}

// OutputSink receives one chunk of a terminal's output, only while that
// terminal is observed (see Attach). It may block while its receiver is
// slow; it returns false once the output can no longer be delivered, which
// ends that terminal's output.
type OutputSink func(terminalID string, chunk []byte) bool

// ExitEvent reports that a shell ended.
type ExitEvent struct {
	TerminalID string
	Exit       Exit
}

// Host owns the shells of one terminal connection.
type Host struct {
	taskRoot    string
	projectRoot string
	shell       string
	log         *slog.Logger
	output      OutputSink
	exits       chan ExitEvent
	launcher    Launcher

	mu        sync.Mutex
	terminals map[string]*Session
}

// NewHost returns a host whose shells run below cfg.TaskRoot and
// cfg.ProjectRoot.
func NewHost(cfg Config, output OutputSink) *Host {
	h := &Host{
		taskRoot:    cfg.TaskRoot,
		projectRoot: cfg.ProjectRoot,
		shell:       cfg.Shell,
		log:         cfg.Logger,
		output:      output,
		launcher:    cfg.Launcher,
		exits:       make(chan ExitEvent, exitQueue),
		terminals:   map[string]*Session{},
	}
	if h.shell == "" {
		h.shell = DefaultShell()
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	return h
}

// Exits reports each shell as it ends, until every shell of the host has.
// A report nobody reads is dropped, not held.
func (h *Host) Exits() <-chan ExitEvent { return h.exits }

// Session is one shell.
type Session struct {
	// ID is how the server names the terminal.
	ID string
	// PID is the shell's process id.
	PID int

	host       *Host
	shell      *shell
	input      chan []byte
	readerDone chan struct{}
	dead       chan struct{}
	// wrote carries a token whenever output is read; repaint waits for it.
	wrote chan struct{}
	// settled is false from the moment output is read until its token is on
	// wrote. A repaint waits for it, so that output which is read but not yet
	// signalled is not taken for a reaction to its resize.
	settled atomic.Bool

	mu        sync.Mutex
	cols      int
	rows      int
	observed  bool
	repaintMu sync.Mutex
}

// Open starts a login shell in cwd at cols by rows, a directory the browser
// named: it has to be in this daemon's task trees or its default project
// folder. The terminal is served once Serve is called.
func (h *Host) Open(cwd string, cols, rows int) (*Session, *Refusal) {
	dir, refusal := ResolveCwd([]string{h.taskRoot, h.projectRoot}, cwd)
	if refusal != nil {
		return nil, refusal
	}
	return h.openShell(dir, cols, rows)
}

// OpenProject starts a login shell in cwd, the local directory a project
// registered, which the server looked up. This daemon's trees do not apply:
// the folder is wherever the project keeps it. It still has to be a directory
// on this machine. The terminal is served once Serve is called.
func (h *Host) OpenProject(cwd string, cols, rows int) (*Session, *Refusal) {
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return nil, refuse(protocol.TerminalErrBadRequest, "cwd must be an absolute path")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, refuse(protocol.TerminalErrBadRequest, "not an existing directory: "+cwd)
	}
	return h.openShell(cwd, cols, rows)
}

// OpenDefaultProject starts a login shell in the default project folder,
// creating the folder if no terminal was opened there before. The terminal is
// served once Serve is called.
func (h *Host) OpenDefaultProject(cols, rows int) (*Session, *Refusal) {
	// Before the folder is created: a size the daemon cannot serve must leave
	// nothing behind.
	if !protocol.ValidTerminalSize(cols, rows) {
		return nil, refuse(protocol.TerminalErrBadRequest, badSizeMessage)
	}
	if err := os.MkdirAll(h.projectRoot, 0o755); err != nil {
		h.log.Warn("terminal: the default project folder could not be created", "path", h.projectRoot, "error", err)
		return nil, refuse(protocol.TerminalErrInternal, "the default project folder could not be created: "+err.Error())
	}
	return h.openShell(h.projectRoot, cols, rows)
}

// openShell runs the shell in dir, which the caller has judged.
func (h *Host) openShell(dir string, cols, rows int) (*Session, *Refusal) {
	if !protocol.ValidTerminalSize(cols, rows) {
		return nil, refuse(protocol.TerminalErrBadRequest, badSizeMessage)
	}
	return h.start([]string{h.shell}, dir, terminalEnv(), cols, rows, "the shell")
}

// OpenCommand runs a program, with its arguments and its whole environment,
// in a terminal in cwd. The terminal root does not apply: the directory is a
// task tree this daemon built and the server named, not a browser's choice.
// It still has to be a directory on this machine. env nil means the daemon's
// environment, as for a shell. The terminal is served once Serve is called.
func (h *Host) OpenCommand(cwd string, argv, env []string, cols, rows int) (*Session, *Refusal) {
	if !protocol.ValidTerminalSize(cols, rows) {
		return nil, refuse(protocol.TerminalErrBadRequest, badSizeMessage)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, refuse(protocol.TerminalErrBadRequest, "no program to run")
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return nil, refuse(protocol.TerminalErrPathNotAllowed, "the directory is not available on this daemon")
	}
	if env == nil {
		env = terminalEnv()
	}
	return h.start(argv, cwd, env, cols, rows, "the agent")
}

// start runs argv in a new terminal. Nothing of the terminal is carried
// until Serve is called.
func (h *Host) start(argv []string, dir string, env []string, cols, rows int, what string) (*Session, *Refusal) {
	sh, err := startProcess(argv, dir, cols, rows, env)
	if err != nil {
		if errors.Is(err, errUnsupportedPlatform) {
			return nil, refuse(protocol.TerminalErrUnsupportedPlatform, err.Error())
		}
		h.log.Warn("terminal: starting failed", "cwd", dir, "program", argv[0], "error", err)
		return nil, refuse(protocol.TerminalErrInternal, what+" could not be started: "+err.Error())
	}

	s := &Session{
		ID:         newID(),
		host:       h,
		shell:      sh,
		input:      make(chan []byte, inputQueue),
		readerDone: make(chan struct{}),
		dead:       make(chan struct{}),
		wrote:      make(chan struct{}, 1),
		cols:       cols,
		rows:       rows,
		observed:   true,
	}
	s.PID = sh.pid()
	s.settled.Store(true)
	h.mu.Lock()
	h.terminals[s.ID] = s
	h.mu.Unlock()
	return s, nil
}

// Serve makes the terminal live: its output is carried, its input written,
// and its exit ends it and is reported. A terminal that is never served runs
// its program on in silence. It is called once the server has been told of
// the terminal, so that no output of it arrives for a terminal the server
// does not know yet.
func (s *Session) Serve() {
	go s.readOutput()
	go s.writeInput()
	go s.reportExit()
}

// terminalEnv is the daemon's environment plus the terminal type the shell
// should assume.
func terminalEnv() []string {
	return append(os.Environ(), "TERM=xterm-256color")
}

const badSizeMessage = "cols and rows must be between 1 and 1000"

// Attach marks the terminal observed, so its output is transported again,
// and asks the program in it to repaint (see Session.repaint).
func (h *Host) Attach(id string) (*Session, *Refusal) {
	s := h.lookup(id)
	if s == nil {
		return nil, refuse(protocol.TerminalErrNotFound, "no such terminal")
	}
	s.mu.Lock()
	s.observed = true
	s.mu.Unlock()
	go s.repaint()
	return s, nil
}

// Detach marks the terminal unobserved: its output is read and dropped from
// now on, so a terminal nobody looks at costs no bandwidth.
func (h *Host) Detach(id string) {
	if s := h.lookup(id); s != nil {
		s.mu.Lock()
		s.observed = false
		s.mu.Unlock()
	}
}

// Resize sets the terminal's size.
func (h *Host) Resize(id string, cols, rows int) {
	if !protocol.ValidTerminalSize(cols, rows) {
		return
	}
	s := h.lookup(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	if err := s.shell.Resize(cols, rows); err != nil {
		h.log.Warn("terminal: resizing failed", "terminal_id", id, "error", err)
	}
}

// Write sends keyboard input to a terminal. Input for an unknown terminal,
// or past what the terminal's queue holds, is dropped.
func (h *Host) Write(id string, input []byte) {
	s := h.lookup(id)
	if s == nil || len(input) == 0 {
		return
	}
	select {
	case s.input <- input:
	default:
		h.log.Debug("terminal: input dropped, the terminal is not reading", "terminal_id", id, "bytes", len(input))
	}
}

// Close terminates a terminal. Its exit is reported on Exits like any other.
func (h *Host) Close(id string) {
	if s := h.lookup(id); s != nil {
		go s.shell.Terminate(closeGrace)
	}
}

// CloseAll terminates every terminal of the host.
func (h *Host) CloseAll() {
	h.mu.Lock()
	sessions := make([]*Session, 0, len(h.terminals))
	for _, s := range h.terminals {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		go s.shell.Terminate(closeGrace)
	}
}

func (h *Host) lookup(id string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminals[id]
}

func (h *Host) exited(s *Session) {
	h.mu.Lock()
	delete(h.terminals, s.ID)
	h.mu.Unlock()
	select {
	case h.exits <- ExitEvent{TerminalID: s.ID, Exit: s.shell.Exit()}:
	default:
		h.log.Debug("terminal: exit not reported", "terminal_id", s.ID)
	}
}

// Done is closed once the terminal has exited and been forgotten by its host.
// What a launch prepared for it can be released then.
func (s *Session) Done() <-chan struct{} { return s.dead }

// Size returns the terminal's current size.
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

func (s *Session) isObserved() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observed
}

// readOutput carries the terminal's output to the host's sink while it is
// observed, and drains and discards it otherwise. It ends when the terminal
// closes or the sink refuses output.
func (s *Session) readOutput() {
	defer close(s.readerDone)
	buf := make([]byte, protocol.MaxTerminalOutputChunk)
	for {
		n, err := s.shell.Read(buf)
		s.settled.Store(false)
		if n > 0 {
			select {
			case s.wrote <- struct{}{}:
			default:
			}
		}
		s.settled.Store(true)
		if n > 0 && s.isObserved() {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if !s.host.output(s.ID, chunk) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// writeInput is the terminal's one writer. It is a goroutine of its own so a
// program that reads no input, and leaves the terminal's input queue full,
// stalls only this terminal and not the connection that carries input for
// all of them.
func (s *Session) writeInput() {
	for {
		select {
		case data := <-s.input:
			if _, err := s.shell.Write(data); err != nil {
				return
			}
		case <-s.dead:
			return
		}
	}
}

// reportExit ends the session once its shell has exited: after the reader
// has carried the shell's last output, or stopped waiting for it.
func (s *Session) reportExit() {
	<-s.shell.Done()
	// The reader ends on its own once nothing holds the terminal open. A
	// background job that still does must not hold the exit back.
	select {
	case <-s.readerDone:
	case <-time.After(outputDrainGrace):
	}
	s.shell.Close()
	<-s.readerDone
	close(s.dead)
	s.host.exited(s)
}

// repaint makes the program in the terminal draw its whole screen again.
// A socket that attached has no scrollback, so it would otherwise see only
// what the program draws next. The terminal is made one row shorter until
// the program has drawn for that, and is then given its size again: ncurses,
// told of one change only, keeps the screen and updates it in place, and two
// changes that both arrive before the program reads the terminal again are
// told as one. Told of a change, a program clears the screen.
//
// The size is given back only once the drawing has stopped. ncurses ignores
// a size change that lands in the middle of a refresh, and would keep the
// size it last saw for good; the shorter size is the one held so that what
// is lost then is a row of the screen, not the bottom of what it draws.
func (s *Session) repaint() {
	s.repaintMu.Lock()
	defer s.repaintMu.Unlock()
	// Output the program wrote before the size changes is still being read
	// when the first one lands, and would be taken for its reaction.
	s.awaitQuiet(repaintHold)
	s.drainWrote()
	cols, rows := s.Size()
	short := rows - 1
	if short < 1 {
		short = rows + 1
	}
	s.shell.Resize(cols, short)
	if s.awaitOutput() {
		s.awaitSettled(repaintHold)
		s.drainWrote()
	}
	// A resize from the socket that attached may have landed meanwhile:
	// return to the current size, not to the one read before.
	cols, rows = s.Size()
	s.shell.Resize(cols, rows)
}

// awaitOutput waits for the program to write something, and reports whether
// it did before the terminal ended or the wait passed.
func (s *Session) awaitOutput() bool {
	timer := time.NewTimer(repaintReaction)
	defer timer.Stop()
	select {
	case <-s.wrote:
		return true
	case <-timer.C:
	case <-s.dead:
	}
	return false
}

// drainWrote drops the token of output that was read before now.
func (s *Session) drainWrote() {
	select {
	case <-s.wrote:
	default:
	}
}

// awaitQuiet waits until the program's output so far is read and has its
// token on wrote, or for max. A token placed after that is for output
// written since, so what follows a resize sent then is a reaction to it.
func (s *Session) awaitQuiet(max time.Duration) {
	deadline := time.Now().Add(max)
	for !s.quiet() {
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitSettled waits for the program's output to end: until it is quiet and
// stays so for repaintSettle, or for max.
func (s *Session) awaitSettled(max time.Duration) {
	deadline := time.Now().Add(max)
	for {
		s.awaitQuiet(time.Until(deadline))
		time.Sleep(min(repaintSettle, time.Until(deadline)))
		if s.quiet() || time.Now().After(deadline) {
			return
		}
	}
}

// quiet reports whether nothing the program wrote is left to be read and
// nothing that was read is left without its token on wrote. A program that
// stays silent is quiet: its reader is settled while it blocks in Read.
func (s *Session) quiet() bool {
	return s.settled.Load() && s.shell.pending() == 0
}
