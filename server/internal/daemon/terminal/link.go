package terminal

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/wsoutbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The frames waiting for the server: output chunks are at most 32 KiB, so
// the byte cap is what a stalled server costs.
const (
	linkQueueFrames  = 256
	linkQueueReserve = 16
	linkQueueBytes   = 8 << 20
)

// Link speaks the daemon's end of one terminal connection. The server's
// frames go in through Handle and HandleBinary; what the daemon answers,
// and the terminals' output, come out on out for the connection's writer.
type Link struct {
	host *Host
	out  *wsoutbox.Outbox
	log  *slog.Logger

	done chan struct{}
	once sync.Once
}

// NewLink returns a link whose terminals run below cfg.TaskRoot and
// cfg.ProjectRoot, and in the project folders the server names.
func NewLink(cfg Config) *Link {
	l := &Link{
		out:  wsoutbox.New(linkQueueFrames, linkQueueReserve, linkQueueBytes),
		done: make(chan struct{}),
		log:  cfg.Logger,
	}
	if l.log == nil {
		l.log = slog.Default()
	}
	cfg.Logger = l.log
	l.host = NewHost(cfg, l.sendOutput)
	go l.forwardExits()
	return l
}

// Handle applies one control message from the server.
func (l *Link) Handle(msg *protocol.DaemonTerminalMessage) {
	select {
	case <-l.done:
		return
	default:
	}
	switch msg.Type {
	case protocol.TerminalMsgOpen:
		l.open(msg)
	case protocol.TerminalMsgAttach:
		s, refusal := l.host.Attach(msg.TerminalID)
		if refusal != nil {
			l.answer(msg.RequestID, refusal)
			return
		}
		cols, rows := s.Size()
		l.sendControl(protocol.DaemonTerminalMessage{
			Type: protocol.TerminalMsgReady, RequestID: msg.RequestID,
			TerminalID: s.ID, PID: s.PID, Cols: cols, Rows: rows,
		})
	case protocol.TerminalMsgResize:
		l.host.Resize(msg.TerminalID, msg.Cols, msg.Rows)
	case protocol.TerminalMsgClose:
		l.host.Close(msg.TerminalID)
	case protocol.DaemonTerminalMsgDetach:
		l.host.Detach(msg.TerminalID)
	default:
		l.log.Warn("terminal: unknown control message from the server", "type", msg.Type)
	}
}

// open answers an open with a shell in the folder it names, or, when it
// carries an agent part, with whatever the launcher makes of that. The
// terminal is served once the answer is queued: output that came first would
// reach the server for a terminal it does not know yet, and be dropped there.
func (l *Link) open(msg *protocol.DaemonTerminalMessage) {
	s, cleanup, refusal := l.start(msg)
	if refusal != nil {
		l.answer(msg.RequestID, refusal)
		return
	}
	// What a launch prepared is needed for exactly as long as the terminal.
	go func() {
		<-s.Done()
		if cleanup != nil {
			cleanup()
		}
	}()
	l.announce(msg.RequestID, s)
	s.Serve()
}

// start returns the unserved terminal an open asks for, and what is to be
// released once it has ended (nil for a shell).
func (l *Link) start(msg *protocol.DaemonTerminalMessage) (*Session, func(), *Refusal) {
	switch {
	case msg.Agent != nil && msg.Project != "":
		return nil, nil, refuse(protocol.TerminalErrBadRequest, "open takes an agent or a project, not both")
	case msg.Agent != nil:
		return l.startAgent(msg)
	case msg.Project == protocol.DaemonTerminalProjectFolder:
		if msg.Cwd == "" {
			return nil, nil, refuse(protocol.TerminalErrBadRequest, "a project folder names no directory")
		}
		s, refusal := l.host.OpenProject(msg.Cwd, msg.Cols, msg.Rows)
		return s, nil, refusal
	case msg.Project == protocol.DaemonTerminalProjectDefault:
		s, refusal := l.host.OpenDefaultProject(msg.Cols, msg.Rows)
		return s, nil, refusal
	case msg.Project != "":
		return nil, nil, refuse(protocol.TerminalErrBadRequest, "unknown project "+msg.Project)
	default:
		s, refusal := l.host.Open(msg.Cwd, msg.Cols, msg.Rows)
		return s, nil, refusal
	}
}

// startAgent returns the unserved terminal of an open that starts an agent.
func (l *Link) startAgent(msg *protocol.DaemonTerminalMessage) (*Session, func(), *Refusal) {
	agent := msg.Agent
	if l.host.launcher == nil {
		return nil, nil, refuse(protocol.TerminalErrInternal, "this daemon cannot start agent sessions")
	}
	if agent.IssueID == "" || agent.StartTreeID == "" || len(agent.Brief) > protocol.MaxTerminalBrief {
		return nil, nil, refuse(protocol.TerminalErrBadRequest, "malformed agent open")
	}
	for _, dir := range agent.OtherDirs {
		if !filepath.IsAbs(dir) {
			return nil, nil, refuse(protocol.TerminalErrBadRequest, "other_dirs must be absolute paths")
		}
	}
	cmd, refusal := l.host.launcher.Launch(msg.Cwd, agent)
	if refusal != nil {
		return nil, nil, refusal
	}
	if cmd == nil || len(cmd.Argv) == 0 || cmd.Argv[0] == "" {
		if cmd != nil && cmd.Cleanup != nil {
			cmd.Cleanup()
		}
		return nil, nil, refuse(protocol.TerminalErrInternal, "the agent could not be prepared")
	}
	s, refusal := l.host.OpenCommand(msg.Cwd, cmd.Argv, cmd.Env, msg.Cols, msg.Rows)
	if refusal != nil {
		if cmd.Cleanup != nil {
			cmd.Cleanup()
		}
		return nil, nil, refusal
	}
	return s, cmd.Cleanup, nil
}

// announce tells the server that an open or an attach is ready.
func (l *Link) announce(requestID string, s *Session) {
	cols, rows := s.Size()
	l.sendControl(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgReady, RequestID: requestID,
		TerminalID: s.ID, PID: s.PID, Cols: cols, Rows: rows,
	})
}

// HandleBinary applies one binary frame from the server: keyboard input.
func (l *Link) HandleBinary(frame []byte) {
	terminalID, input, err := protocol.DecodeTerminalBinary(frame)
	if err != nil {
		l.log.Warn("terminal: malformed binary frame from the server", "error", err)
		return
	}
	l.host.Write(terminalID, input)
}

// Close ends every terminal of the link. What follows on out is what the
// writer already had; the exits of the terminals Close ended are not
// reported, since the connection is over.
func (l *Link) Close() {
	l.once.Do(func() { close(l.done) })
	l.host.CloseAll()
}

// answer refuses an open or an attach with the refusal's code and message.
func (l *Link) answer(requestID string, refusal *Refusal) {
	l.sendControl(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgError, RequestID: requestID,
		Code: refusal.Code, Message: refusal.Message,
	})
}

func (l *Link) sendControl(msg protocol.DaemonTerminalMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		l.log.Error("terminal: encoding a control message", "error", err)
		return
	}
	if !l.out.TrySend(wsoutbox.Text(data), true) {
		l.log.Warn("terminal: a control message did not fit the queue", "type", msg.Type)
	}
}

// sendOutput queues one chunk of terminal output. While the queue is full it
// waits, which stops the terminal's reader and so throttles the program: a
// stalled link to the server slows the shell rather than growing memory. It
// returns false once the link is closed.
func (l *Link) sendOutput(terminalID string, chunk []byte) bool {
	f := wsoutbox.Binary(protocol.EncodeTerminalBinary(terminalID, chunk))
	for {
		if l.out.TrySend(f, false) {
			return true
		}
		if !l.out.WaitRoom(time.Now().Add(time.Hour), l.done) {
			return false
		}
	}
}

// forwardExits reports each shell's exit to the server.
func (l *Link) forwardExits() {
	for {
		select {
		case event := <-l.host.Exits():
			l.sendControl(protocol.DaemonTerminalMessage{
				Type: protocol.TerminalMsgExit, TerminalID: event.TerminalID,
				ExitCode: event.Exit.Code, Signal: event.Exit.Signal,
			})
		case <-l.done:
			return
		}
	}
}

// ServeWS serves the daemon's end of the terminal connection until conn
// closes or ctx is cancelled, then ends every terminal opened over it.
func ServeWS(ctx context.Context, conn *websocket.Conn, cfg Config) {
	link := NewLink(cfg)
	defer link.Close()

	go wsoutbox.WritePump(conn, link.out, link.done)
	go func() {
		select {
		case <-ctx.Done():
			link.Close()
			conn.Close()
		case <-link.done:
		}
	}()
	wsoutbox.PrepareRead(conn, protocol.MaxTerminalInputChunk+1024)

	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch typ {
		case websocket.TextMessage:
			var msg protocol.DaemonTerminalMessage
			// An agent open carries the task brief, so it is the one large frame.
			if len(data) > maxControlFrame+protocol.MaxTerminalBrief || json.Unmarshal(data, &msg) != nil {
				link.log.Warn("terminal: unreadable control message from the server")
				continue
			}
			link.Handle(&msg)
		case websocket.BinaryMessage:
			link.HandleBinary(data)
		}
	}
}
