package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// DaemonCapabilityTerminalV1 advertises that the daemon hosts interactive
// shells for the web terminal and keeps a dedicated connection open to the
// server (GET /api/daemon/terminal/ws).
//
// The server lists it in heartbeat acks only while the terminal_v1 flag is on,
// and the daemon dials the terminal connection only after an ack lists it: a
// daemon talking to an older server, or one whose deployment keeps the flag
// off, never opens the connection. Terminal traffic never rides the control
// connection, because that hub is best-effort and keystrokes are not.
const DaemonCapabilityTerminalV1 = "terminal-v1"

// DaemonTerminalMsgDetach is the one control message that exists only on the
// daemon link: the last browser socket left the terminal, so the daemon stops
// transporting its output until the next attach. It is absent from the
// browser protocol, where a socket simply closes.
const DaemonTerminalMsgDetach = "detach"

// Size limits for a terminal. A browser window never reaches them; they exist
// so a hostile or buggy client cannot ask the daemon for an absurd winsize.
const (
	MaxTerminalCols = 1000
	MaxTerminalRows = 1000
)

// Payload limits on the terminal sockets, shared by both ends so each sets a
// read limit that admits what the other sends. The daemon cuts PTY output
// into chunks of at most MaxTerminalOutputChunk, which bounds a browser
// socket's queue in bytes and not only in frames; a paste may be as large as
// MaxTerminalInputChunk.
const (
	MaxTerminalOutputChunk = 32 << 10
	MaxTerminalInputChunk  = 256 << 10

	// MaxTerminalBrief bounds the task brief of an agent open, which rides a
	// control frame from the server to the daemon.
	MaxTerminalBrief = 32 << 10
)

// ValidTerminalSize reports whether cols and rows are both positive and no
// larger than the maximum.
func ValidTerminalSize(cols, rows int) bool {
	return cols > 0 && cols <= MaxTerminalCols && rows > 0 && rows <= MaxTerminalRows
}

// Message types exchanged between the browser and the server (terminalrelay
// is the server side). Text frames carry one message as JSON; binary frames
// carry raw terminal bytes: client→server is keyboard input, server→client is
// terminal output. Direction tells them apart.
const (
	// Browser → server.
	TerminalMsgOpen   = "open"
	TerminalMsgAttach = "attach"
	TerminalMsgResize = "resize"
	TerminalMsgClose  = "close"
	// Server → browser.
	TerminalMsgReady = "ready"
	TerminalMsgExit  = "exit"
	TerminalMsgError = "error"
)

// Error codes carried by a terminal "error" message. The daemon uses the same
// vocabulary when it refuses an open or an attach, so the relay can pass the
// code through.
const (
	TerminalErrFlagDisabled             = "flag_disabled"
	TerminalErrBadRequest               = "bad_request"
	TerminalErrTerminalRootUnconfigured = "terminal_root_unconfigured"
	TerminalErrPathNotAllowed           = "path_not_allowed"
	TerminalErrDaemonUnavailable        = "daemon_unavailable"
	TerminalErrNotFound                 = "not_found"
	TerminalErrUnsupportedPlatform      = "unsupported_platform"
	TerminalErrInternal                 = "internal"
	// Agent terminals: the task tree is not built, or the daemon that holds
	// it has no terminal connection to the server.
	TerminalErrTreeNotReady   = "tree_not_ready"
	TerminalErrRuntimeOffline = "runtime_offline"
)

// KnownTerminalError reports whether code is one of the contract's codes. A
// code from elsewhere is reported to the browser as "internal", so the set a
// client has to understand stays closed.
func KnownTerminalError(code string) bool {
	switch code {
	case TerminalErrFlagDisabled, TerminalErrBadRequest, TerminalErrTerminalRootUnconfigured,
		TerminalErrPathNotAllowed, TerminalErrDaemonUnavailable, TerminalErrNotFound,
		TerminalErrUnsupportedPlatform, TerminalErrInternal,
		TerminalErrTreeNotReady, TerminalErrRuntimeOffline:
		return true
	}
	return false
}

// BrowserAgentRef names the task tree an agent terminal starts in. Both are
// UUIDs: the issue's and the tree's.
type BrowserAgentRef struct {
	IssueID string `json:"issue_id"`
	TreeID  string `json:"tree_id"`
}

// BrowserTerminalMessage is the decode envelope for everything a browser may
// send, and for the messages the server sends back. Which fields matter
// depends on Type; the per-type structs below are what the server encodes.
type BrowserTerminalMessage struct {
	Type string `json:"type"`

	// open: Cwd for a shell, Agent for an agent session; never both
	Cwd      string           `json:"cwd,omitempty"`
	Agent    *BrowserAgentRef `json:"agent,omitempty"`
	Cols     int              `json:"cols,omitempty"`
	Rows     int              `json:"rows,omitempty"`
	DaemonID string           `json:"daemonId,omitempty"`

	// attach, close; the id of the terminal on ready and exit
	TerminalID string `json:"terminalId,omitempty"`

	// ready
	PID int `json:"pid,omitempty"`

	// exit: ExitCode is nil when a signal ended the process.
	ExitCode *int   `json:"exitCode,omitempty"`
	Signal   string `json:"signal,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// TerminalReady answers a successful open or attach. Output starts streaming
// after it.
type TerminalReady struct {
	Type       string `json:"type"`
	TerminalID string `json:"terminalId"`
	PID        int    `json:"pid"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
}

// TerminalExit reports that the shell ended. ExitCode is null when a signal
// ended the process, and Signal then names it.
type TerminalExit struct {
	Type       string `json:"type"`
	TerminalID string `json:"terminalId"`
	ExitCode   *int   `json:"exitCode"`
	Signal     string `json:"signal,omitempty"`
}

// TerminalError reports a failure. After an error answering open or attach the
// server closes the socket.
type TerminalError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DaemonTerminalMessage is one control message on the dedicated
// server↔daemon terminal connection. Text frames carry one as JSON (snake_case
// like the rest of the daemon protocol); binary frames are addressed with
// EncodeTerminalBinary. The server dials nothing: the daemon connects, and
// the server asks it to act.
//
// Server → daemon: open, attach, detach, resize, close.
// Daemon → server: ready (the answer to open and to attach), exit, error.
type DaemonTerminalMessage struct {
	Type string `json:"type"`

	// open, attach; ready and error echo it back
	RequestID string `json:"request_id,omitempty"`
	// every type but open
	TerminalID string `json:"terminal_id,omitempty"`

	Cwd    string `json:"cwd,omitempty"`  // open
	Cols   int    `json:"cols,omitempty"` // open, resize, ready
	Rows   int    `json:"rows,omitempty"` // open, resize, ready
	PID    int    `json:"pid,omitempty"`  // ready
	Signal string `json:"signal,omitempty"`
	Code   string `json:"code,omitempty"` // error
	// ExitCode is nil when a signal ended the process; it is absent from
	// every other message, which decodes as the same nil.
	ExitCode *int   `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"` // error

	// Agent is set on an open that starts an agent CLI instead of a shell.
	// Cwd is then the start tree's path.
	Agent *DaemonTerminalAgent `json:"agent,omitempty"`
}

// DaemonTerminalAgent is the agent part of an open: what the daemon needs to
// launch the agent CLI for a task, resolved and rendered by the server.
type DaemonTerminalAgent struct {
	IssueID     string `json:"issue_id"`
	StartTreeID string `json:"start_tree_id"`
	// OtherDirs are the absolute paths of the task's other ready trees on
	// this daemon, which the CLI is given access to as well.
	OtherDirs []string `json:"other_dirs"`
	// Brief is the rendered task description, up to MaxTerminalBrief bytes.
	Brief string `json:"brief"`
}

const (
	terminalBinaryHeaderLen = 2
	// MaxTerminalIDLen bounds the terminal ID inside a binary frame, so a
	// truncated or hostile frame cannot make the decoder trust a huge length.
	MaxTerminalIDLen = 255
)

var errTerminalBinaryFrame = errors.New("malformed terminal binary frame")

// EncodeTerminalBinary builds a binary frame addressed to one terminal. The
// returned slice shares payload's tail; do not modify either afterwards.
//
// Layout: a 2-byte big-endian length, the UTF-8 terminal ID, then the payload.
// An address is needed on every binary frame because one connection carries
// several terminals, and the browser protocol carries no ids on binary frames
// (a socket is bound to at most one terminal), so the relay adds it.
func EncodeTerminalBinary(terminalID string, payload []byte) []byte {
	id := []byte(terminalID)
	frame := make([]byte, terminalBinaryHeaderLen+len(id)+len(payload))
	binary.BigEndian.PutUint16(frame, uint16(len(id)))
	copy(frame[terminalBinaryHeaderLen:], id)
	copy(frame[terminalBinaryHeaderLen+len(id):], payload)
	return frame
}

// DecodeTerminalBinary splits a binary frame into its terminal ID and payload.
// The payload aliases frame.
func DecodeTerminalBinary(frame []byte) (terminalID string, payload []byte, err error) {
	if len(frame) < terminalBinaryHeaderLen {
		return "", nil, fmt.Errorf("%w: %d bytes, shorter than the header", errTerminalBinaryFrame, len(frame))
	}
	n := int(binary.BigEndian.Uint16(frame))
	if n == 0 || n > MaxTerminalIDLen || terminalBinaryHeaderLen+n > len(frame) {
		return "", nil, fmt.Errorf("%w: terminal id length %d in a %d-byte frame", errTerminalBinaryFrame, n, len(frame))
	}
	id := frame[terminalBinaryHeaderLen : terminalBinaryHeaderLen+n]
	payload = frame[terminalBinaryHeaderLen+n:]
	return string(id), payload, nil
}
