package terminal

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/wsoutbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// daemonConn is one daemon's terminal connection. A daemon that reconnects
// replaces its previous connection; the terminals of the old one are gone
// with it, because they lived in that daemon process's memory.
type daemonConn struct {
	relay    *Terminal
	Identity DaemonIdentity
	conn     *websocket.Conn
	out      *wsoutbox.Outbox

	done     chan struct{}
	doneOnce sync.Once
	closed   bool // guarded by relay.mu

	// terminals are the terminals hosted over this connection. Guarded by
	// relay.mu.
	terminals map[string]*terminal
}

// terminal is one shell, or one agent session, on a daemon.
type terminal struct {
	ID     string
	daemon *daemonConn // nil once the terminal has ended
	// owner is the user who opened it, and the only one who may attach to it.
	// A shell's daemon is its owner's, so this is the daemon's user then; an
	// agent session runs on the daemon that built its tree, which may be
	// another member's.
	owner string
	// attached are the browser sockets bound to it, which receive its
	// output. Empty means the daemon is told to stop transporting output.
	attached map[*browserConn]struct{}
	// outputSeen is set once output has been forwarded to a socket, which is
	// how the relay tells a shell somebody has seen from one whose opener
	// left before anything reached it.
	outputSeen bool
}

type pendingKind int

const (
	pendingOpen pendingKind = iota
	pendingAttach
)

// pendingRequest is an open or an attach a daemon has not answered yet.
type pendingRequest struct {
	browser  *browserConn
	daemon   *daemonConn
	kind     pendingKind
	terminal *terminal // set for an attach
	timer    *time.Timer
}

// ServeDaemon serves an upgraded daemon connection until it closes.
func (t *Terminal) ServeDaemon(conn *websocket.Conn, identity DaemonIdentity) {
	d := &daemonConn{
		relay:     t,
		Identity:  identity,
		conn:      conn,
		out:       wsoutbox.New(daemonOutboxFrames, daemonOutboxReserve, daemonOutboxBytes),
		done:      make(chan struct{}),
		terminals: map[string]*terminal{},
	}
	key := daemonKey{userID: identity.UserID, daemonID: identity.DaemonID}

	t.mu.Lock()
	if prev := t.daemons[key]; prev != nil {
		t.daemonGoneLocked(prev)
		prev.markClosed()
	}
	t.daemons[key] = d
	t.mu.Unlock()

	go wsoutbox.WritePump(conn, d.out, d.done)
	d.readLoop()

	t.mu.Lock()
	defer t.mu.Unlock()
	d.markClosed()
	if t.daemons[key] == d {
		delete(t.daemons, key)
	}
	t.daemonGoneLocked(d)
}

// markClosed ends the connection for good: its writer stops and the socket is
// closed at once, so a replaced connection cannot keep answering. The caller
// holds relay.mu.
func (d *daemonConn) markClosed() {
	if d.closed {
		return
	}
	d.closed = true
	d.doneOnce.Do(func() { close(d.done) })
	d.conn.Close()
}

func (d *daemonConn) readLoop() {
	conn := d.conn
	// The daemon sends output chunks and relays browser input, so the limit
	// admits the larger of the two.
	wsoutbox.PrepareRead(conn, protocol.MaxTerminalInputChunk+1024)
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch typ {
		case websocket.TextMessage:
			d.handleControl(data)
		case websocket.BinaryMessage:
			d.handleOutput(data)
		}
	}
}

func (d *daemonConn) handleControl(data []byte) {
	t := d.relay
	var msg protocol.DaemonTerminalMessage
	if len(data) > maxControlFrame || json.Unmarshal(data, &msg) != nil {
		t.log.Warn("terminal: daemon sent an unreadable control message", "daemon_id", d.Identity.DaemonID)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if d.closed {
		return
	}
	switch msg.Type {
	case protocol.TerminalMsgReady:
		t.daemonReadyLocked(d, &msg)
	case protocol.TerminalMsgExit:
		t.daemonExitLocked(d, &msg)
	case protocol.TerminalMsgError:
		t.daemonErrorLocked(d, &msg)
	default:
		t.log.Warn("terminal: daemon sent an unknown control message", "daemon_id", d.Identity.DaemonID, "type", msg.Type)
	}
}

func (d *daemonConn) handleOutput(data []byte) {
	t := d.relay
	terminalID, payload, err := protocol.DecodeTerminalBinary(data)
	if err != nil {
		t.log.Warn("terminal: daemon sent a malformed binary frame", "daemon_id", d.Identity.DaemonID, "error", err)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.outputLocked(d, terminalID, payload)
}

// outputLocked forwards one chunk of terminal output to the sockets attached
// to that terminal. The caller holds t.mu.
func (t *Terminal) outputLocked(d *daemonConn, terminalID string, payload []byte) {
	if d.closed {
		return
	}
	target := d.terminals[terminalID]
	if target == nil {
		return
	}
	// One frame for every attached socket: payload aliases the frame the
	// read loop read, which it hands to nobody else.
	f := wsoutbox.Binary(payload)
	target.outputSeen = true
	for b := range target.attached {
		if !b.out.TrySend(f, false) {
			t.failLocked(b, protocol.TerminalErrInternal, "terminal output was not read fast enough; reattach to continue")
		}
	}
}

// sendLocked queues a control message for the daemon and reports whether it
// fit. The caller holds t.mu.
func (t *Terminal) sendLocked(d *daemonConn, msg protocol.DaemonTerminalMessage) bool {
	data, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	return d.out.TrySend(wsoutbox.Text(data), true)
}

func (d *daemonConn) sendLocked(msg protocol.DaemonTerminalMessage) bool {
	return d.relay.sendLocked(d, msg)
}

// requestLocked sends an open or an attach to a daemon and records who is
// waiting for the answer. The caller holds t.mu.
func (t *Terminal) requestLocked(d *daemonConn, pr *pendingRequest, msg protocol.DaemonTerminalMessage) {
	id := t.newID()
	msg.RequestID = id
	pr.daemon = d
	pr.timer = time.AfterFunc(t.openTimeout, func() { t.expirePending(id) })
	t.pending[id] = pr
	if !t.sendLocked(d, msg) {
		t.deletePendingLocked(id)
		t.failLocked(pr.browser, protocol.TerminalErrDaemonUnavailable, "the daemon connection is busy")
	}
}

// expirePending fails a request the daemon has not answered in time.
func (t *Terminal) expirePending(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	pr := t.pending[id]
	if pr == nil {
		return
	}
	t.deletePendingLocked(id)
	t.failLocked(pr.browser, protocol.TerminalErrDaemonUnavailable, "the daemon did not answer in time")
}

func (t *Terminal) deletePendingLocked(id string) {
	if pr := t.pending[id]; pr != nil {
		pr.timer.Stop()
		delete(t.pending, id)
	}
}

// daemonReadyLocked binds the socket waiting for an open or an attach.
func (t *Terminal) daemonReadyLocked(d *daemonConn, msg *protocol.DaemonTerminalMessage) {
	pr := t.pending[msg.RequestID]
	if pr == nil || pr.daemon != d {
		t.unclaimedLocked(d, msg.TerminalID)
		return
	}
	t.deletePendingLocked(msg.RequestID)
	b := pr.browser
	if msg.TerminalID == "" || !protocol.ValidTerminalSize(msg.Cols, msg.Rows) {
		if msg.TerminalID != "" {
			d.sendLocked(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: msg.TerminalID})
		}
		t.failLocked(b, protocol.TerminalErrInternal, "the daemon sent an invalid answer")
		return
	}
	if b.closed {
		t.unclaimedLocked(d, msg.TerminalID)
		return
	}

	target := pr.terminal
	if pr.kind == pendingOpen {
		if t.terminals[msg.TerminalID] != nil {
			d.sendLocked(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: msg.TerminalID})
			t.failLocked(b, protocol.TerminalErrInternal, "the daemon reused a terminal id")
			return
		}
		target = &terminal{ID: msg.TerminalID, daemon: d, owner: b.userID, attached: map[*browserConn]struct{}{}}
		t.terminals[target.ID] = target
		d.terminals[target.ID] = target
	}
	target.attached[b] = struct{}{}
	b.terminal = target
	if !t.queueLocked(b, protocol.TerminalReady{
		Type:       protocol.TerminalMsgReady,
		TerminalID: target.ID,
		PID:        msg.PID,
		Cols:       msg.Cols,
		Rows:       msg.Rows,
	}) {
		t.endTerminalWithLocked(target, protocol.TerminalErrInternal, "the ready message could not be queued")
	}
}

// unclaimedLocked undoes a shell nobody is waiting for: the browser that
// asked for it left, or the answer came too late. An open nobody claims would
// run unattended, so it is closed; an attach would keep the terminal
// transporting output to nobody, so it is detached. The caller holds t.mu.
func (t *Terminal) unclaimedLocked(d *daemonConn, terminalID string) {
	target := t.terminals[terminalID]
	if target == nil || target.daemon != d {
		// The answer was to an open. The shell is new, and the only one who
		// could have learned its id is gone.
		d.sendLocked(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: terminalID})
		return
	}
	// The answer was to an attach: what the daemon hears is what it would
	// hear if the socket had left a terminal it was attached to.
	t.socketLeftLocked(target)
}

func (t *Terminal) daemonErrorLocked(d *daemonConn, msg *protocol.DaemonTerminalMessage) {
	pr := t.pending[msg.RequestID]
	if pr == nil || pr.daemon != d {
		return
	}
	t.deletePendingLocked(msg.RequestID)
	code := msg.Code
	if !protocol.KnownTerminalError(code) {
		code = protocol.TerminalErrInternal
	}
	if pr.kind == pendingAttach {
		// The daemon no longer has this terminal, so nobody is attached to
		// anything.
		t.endTerminalWithLocked(pr.terminal, code, msg.Message)
	}
	t.failLocked(pr.browser, code, msg.Message)
}

// daemonExitLocked tells every attached socket how the shell ended and
// closes them.
func (t *Terminal) daemonExitLocked(d *daemonConn, msg *protocol.DaemonTerminalMessage) {
	target := d.terminals[msg.TerminalID]
	if target == nil {
		return
	}
	t.endTerminalLocked(target)
	for b := range target.attached {
		t.sendBrowserLocked(b, protocol.TerminalExit{
			Type:       protocol.TerminalMsgExit,
			TerminalID: target.ID,
			ExitCode:   msg.ExitCode,
			Signal:     msg.Signal,
		})
	}
}

// daemonGoneLocked ends everything a daemon connection still hosted: a
// daemon that disconnects takes its terminals with it, and the sockets
// attached to them learn why. The caller holds t.mu.
func (t *Terminal) daemonGoneLocked(d *daemonConn) {
	for id, pr := range t.pending {
		if pr.daemon == d {
			t.deletePendingLocked(id)
			t.failLocked(pr.browser, protocol.TerminalErrDaemonUnavailable, "the daemon disconnected")
		}
	}
	for _, target := range d.terminals {
		t.endTerminalWithLocked(target, protocol.TerminalErrDaemonUnavailable, "the daemon disconnected")
	}
	clear(d.terminals)
}

// queueLocked puts one control message on a browser socket's queue and
// reports whether it fit. The caller holds t.mu.
func (t *Terminal) queueLocked(b *browserConn, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		t.log.Error("terminal: encoding a browser message", "error", err)
		return false
	}
	return b.out.TrySend(wsoutbox.Text(data), true)
}

// sendBrowserLocked queues a control message for a browser socket and
// closes the socket after it: the message is an exit or an error, the last
// thing a socket hears. The caller holds t.mu.
func (t *Terminal) sendBrowserLocked(b *browserConn, v any) {
	if b.closed {
		return
	}
	t.detachLocked(b)
	t.queueLocked(b, v)
	b.markClosed()
}

// failLocked answers a browser socket with an error and closes it.
func (t *Terminal) failLocked(b *browserConn, code, msg string) {
	t.sendBrowserLocked(b, protocol.TerminalError{Type: protocol.TerminalMsgError, Code: code, Message: msg})
}

// endTerminalWithLocked ends a terminal that did not exit on its own (its
// daemon is gone or no longer knows it) and fails the sockets attached to it.
func (t *Terminal) endTerminalWithLocked(target *terminal, code, msg string) {
	if target == nil {
		return
	}
	t.endTerminalLocked(target)
	for b := range target.attached {
		t.failLocked(b, code, msg)
	}
}

// endTerminalLocked removes a terminal from both indexes. The caller holds
// t.mu.
func (t *Terminal) endTerminalLocked(target *terminal) {
	if target.daemon == nil {
		return
	}
	delete(t.terminals, target.ID)
	delete(target.daemon.terminals, target.ID)
	target.daemon = nil
}

// detachLocked unbinds a browser socket. When it was the terminal's last
// socket, the daemon is told to stop transporting output. The caller holds
// t.mu.
func (t *Terminal) detachLocked(b *browserConn) {
	old := b.terminal
	if old == nil {
		return
	}
	b.terminal = nil
	delete(old.attached, b)
	t.socketLeftLocked(old)
}

// socketLeftLocked is what the daemon hears once a socket is no longer
// attached to target: nothing while another socket still is. A shell nobody
// has seen anything of is closed, since nobody knows its id and it could
// never be attached to; otherwise the daemon stops carrying its output.
func (t *Terminal) socketLeftLocked(target *terminal) {
	if target.daemon == nil || len(target.attached) > 0 {
		return
	}
	if !target.outputSeen {
		target.daemon.sendLocked(protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: target.ID})
		return
	}
	target.daemon.sendLocked(protocol.DaemonTerminalMessage{Type: protocol.DaemonTerminalMsgDetach, TerminalID: target.ID})
}
