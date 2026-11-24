package terminal

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/wsoutbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// badSizeMessage answers a size outside protocol.MaxTerminalCols/Rows.
var badSizeMessage = "cols and rows must be between 1 and " + strconv.Itoa(protocol.MaxTerminalCols)

// browserConn is one browser socket. It is bound to at most one terminal,
// and only once the daemon has confirmed the open or attach: until then input
// from it has nowhere to go and is ignored.
type browserConn struct {
	relay  *Terminal
	conn   *websocket.Conn
	userID string
	out    *wsoutbox.Outbox

	// done is closed exactly once, under relay.mu, when the socket must not
	// deliver anything more: the browser went away, its terminal ended, or
	// it failed something.
	done     chan struct{}
	doneOnce sync.Once

	// closed mirrors "done is closed" for goroutines that hold relay.mu.
	closed bool
	// terminal is the bound terminal; nil until ready and after detach.
	terminal *terminal
}

// ServeBrowser serves an upgraded browser socket until it closes.
func (t *Terminal) ServeBrowser(conn *websocket.Conn, userID string) {
	b := &browserConn{
		relay:  t,
		conn:   conn,
		userID: userID,
		out:    wsoutbox.New(browserOutboxFrames, browserOutboxReserve, browserOutboxBytes),
		done:   make(chan struct{}),
	}
	go wsoutbox.WritePump(conn, b.out, b.done)
	b.readLoop()

	t.mu.Lock()
	defer t.mu.Unlock()
	b.markClosed()
	t.browserLeftLocked(b)
}

// markClosed ends the socket. The caller holds relay.mu.
func (b *browserConn) markClosed() {
	if b.closed {
		return
	}
	b.closed = true
	b.doneOnce.Do(func() { close(b.done) })
}

func (b *browserConn) readLoop() {
	conn := b.conn
	wsoutbox.PrepareRead(conn, protocol.MaxTerminalInputChunk+1024)
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch typ {
		case websocket.TextMessage:
			if !b.handleControl(data) {
				return
			}
		case websocket.BinaryMessage:
			if !b.relay.forwardInput(b, data) {
				return
			}
		}
	}
}

// handleControl applies one control message and reports whether the socket
// is still open afterwards.
func (b *browserConn) handleControl(data []byte) bool {
	t := b.relay
	if len(data) > maxControlFrame {
		b.fail(protocol.TerminalErrBadRequest, "control message too large")
		return false
	}
	var msg protocol.BrowserTerminalMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		b.fail(protocol.TerminalErrBadRequest, "malformed control message")
		return false
	}

	switch {
	case msg.Type == protocol.TerminalMsgOpen && msg.Agent != nil:
		// Resolving the tree reads the database, which is not done under the lock.
		return b.openAgent(&msg)
	case msg.Type == protocol.TerminalMsgOpen && msg.Project != "":
		return b.openProject(&msg)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if b.closed {
		return false
	}
	switch msg.Type {
	case protocol.TerminalMsgOpen:
		t.openLocked(b, &msg)
	case protocol.TerminalMsgAttach:
		t.attachLocked(b, msg.TerminalID)
	case protocol.TerminalMsgResize:
		t.resizeLocked(b, msg.Cols, msg.Rows)
	case protocol.TerminalMsgClose:
		t.closeLocked(b, msg.TerminalID)
	default:
		t.failLocked(b, protocol.TerminalErrBadRequest, "unknown message type")
	}
	return !b.closed
}

// fail answers the browser with an error and closes its socket.
func (b *browserConn) fail(code, msg string) {
	b.relay.mu.Lock()
	defer b.relay.mu.Unlock()
	b.relay.failLocked(b, code, msg)
}

// openLocked starts a shell in a directory the browser named, on one of the
// user's daemons, and binds the socket to it once the daemon answers ready.
// An open that names a task tree instead is openAgent; one that names a
// project is openProject.
func (t *Terminal) openLocked(b *browserConn, msg *protocol.BrowserTerminalMessage) {
	if b.terminal != nil {
		t.failLocked(b, protocol.TerminalErrBadRequest, "socket is already bound to a terminal")
		return
	}
	if !validCwd(msg.Cwd) {
		t.failLocked(b, protocol.TerminalErrBadRequest, "cwd must be an absolute, clean path")
		return
	}
	if !protocol.ValidTerminalSize(msg.Cols, msg.Rows) {
		t.failLocked(b, protocol.TerminalErrBadRequest, badSizeMessage)
		return
	}
	t.requestOpenLocked(b, msg.DaemonID, protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgOpen,
		Cwd:  msg.Cwd,
		Cols: msg.Cols,
		Rows: msg.Rows,
	})
}

// requestOpenLocked asks a daemon to start what open describes for the socket:
// the daemon named in the open, else the user's only one. The socket is bound
// to the terminal once the daemon answers ready. The caller holds t.mu, and has
// already checked that the socket is free and the size valid.
func (t *Terminal) requestOpenLocked(b *browserConn, daemonID string, open protocol.DaemonTerminalMessage) {
	d := t.pickDaemonLocked(b.userID, daemonID)
	if d == nil {
		t.failLocked(b, protocol.TerminalErrDaemonUnavailable, "no connected daemon of yours hosts terminals")
		return
	}
	t.requestLocked(d, &pendingRequest{browser: b, kind: pendingOpen}, open)
}

// attachLocked binds the socket to a terminal that already exists.
func (t *Terminal) attachLocked(b *browserConn, terminalID string) {
	if b.terminal != nil {
		t.failLocked(b, protocol.TerminalErrBadRequest, "socket is already bound to a terminal")
		return
	}
	if terminalID == "" {
		t.failLocked(b, protocol.TerminalErrBadRequest, "terminalId is required")
		return
	}
	// Another user's terminal reads as absent: its id is not enumerable.
	target := t.terminals[terminalID]
	if target == nil || target.daemon == nil || target.owner != b.userID {
		t.failLocked(b, protocol.TerminalErrNotFound, "no such terminal")
		return
	}
	t.requestLocked(target.daemon, &pendingRequest{browser: b, kind: pendingAttach, terminal: target},
		protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: terminalID})
}

// resizeLocked resizes the socket's terminal. A resize that arrives before
// the socket is bound (the open has not been answered yet) changes nothing:
// the shell starts at the size the open asked for.
func (t *Terminal) resizeLocked(b *browserConn, cols, rows int) {
	if !protocol.ValidTerminalSize(cols, rows) {
		t.failLocked(b, protocol.TerminalErrBadRequest, badSizeMessage)
		return
	}
	if b.terminal == nil {
		return
	}
	b.terminal.daemon.sendLocked(protocol.DaemonTerminalMessage{
		Type:       protocol.TerminalMsgResize,
		TerminalID: b.terminal.ID,
		Cols:       cols,
		Rows:       rows,
	})
}

// closeLocked terminates a terminal. Its attached sockets learn the outcome
// from the daemon's exit message. A terminal that is gone, or belongs to
// another user, is left alone without an answer: closing what is already
// closed is not an error.
func (t *Terminal) closeLocked(b *browserConn, terminalID string) {
	target := t.terminals[terminalID]
	if target == nil || target.daemon == nil || target.owner != b.userID {
		return
	}
	target.daemon.sendLocked(protocol.DaemonTerminalMessage{
		Type:       protocol.TerminalMsgClose,
		TerminalID: terminalID,
	})
}

// pickDaemonLocked returns the daemon an open should run on: the one named
// in the request, or the user's only one.
func (t *Terminal) pickDaemonLocked(userID, daemonID string) *daemonConn {
	if daemonID != "" {
		return t.daemons[daemonKey{userID: userID, daemonID: daemonID}]
	}
	var found *daemonConn
	for key, d := range t.daemons {
		if key.userID != userID {
			continue
		}
		if found != nil {
			return nil
		}
		found = d
	}
	return found
}

// forwardInput sends keyboard bytes to the socket's terminal and reports
// whether the socket is still open. Input that arrives before the socket is
// bound is ignored. When the daemon's outbound queue has no room the socket
// waits for it, so a paste applies backpressure to its own browser instead
// of being dropped; a socket that waits past inputStallTimeout is detached.
func (t *Terminal) forwardInput(b *browserConn, data []byte) bool {
	deadline := time.Now().Add(inputStallTimeout)
	for {
		t.mu.Lock()
		if b.closed {
			t.mu.Unlock()
			return false
		}
		target := b.terminal
		if target == nil {
			t.mu.Unlock()
			return true
		}
		d := target.daemon
		ok := d.out.TrySend(wsoutbox.Binary(protocol.EncodeTerminalBinary(target.ID, data)), false)
		t.mu.Unlock()
		if ok {
			return true
		}
		if !d.out.WaitRoom(deadline, b.done) {
			t.mu.Lock()
			t.failLocked(b, protocol.TerminalErrDaemonUnavailable, "the daemon connection stalled; reattach to continue")
			t.mu.Unlock()
			return false
		}
	}
}

// browserLeftLocked forgets a browser socket whose reader has ended. The
// caller holds t.mu.
func (t *Terminal) browserLeftLocked(b *browserConn) {
	b.markClosed()
	for id, pr := range t.pending {
		if pr.browser == b {
			// The request is already at the daemon. When its answer arrives
			// nobody claims it and daemonReadyLocked undoes it.
			t.deletePendingLocked(id)
		}
	}
	t.detachLocked(b)
}
