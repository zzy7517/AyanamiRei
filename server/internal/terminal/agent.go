package terminal

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// AgentLaunch is what an agent open resolved to: where the session starts,
// on which daemon, and what that daemon needs to launch the CLI.
type AgentLaunch struct {
	// DaemonID is the daemon that built the tree and will run the session.
	DaemonID string
	// Cwd is the start tree's absolute path.
	Cwd string
	// Agent is the agent part of the open the daemon receives.
	Agent protocol.DaemonTerminalAgent
}

// AgentResolver loads the task tree an agent open names, for the user who
// asked. Exactly one of its results is non-nil: a refusal reaches the browser
// with its code, which has to be one of the contract's.
type AgentResolver interface {
	ResolveAgent(ctx context.Context, userID string, ref protocol.BrowserAgentRef) (*AgentLaunch, *protocol.TerminalError)
}

// validAgentRef reports whether ref names an issue and a tree at all. What the
// ids mean is the resolver's to decide.
func validAgentRef(ref *protocol.BrowserAgentRef) bool {
	const maxIDLen = 128
	return ref != nil && ref.IssueID != "" && len(ref.IssueID) <= maxIDLen && ref.TreeID != "" && len(ref.TreeID) <= maxIDLen
}

// validLaunch reports whether the daemon can be given launch as it is.
func validLaunch(launch *AgentLaunch) bool {
	if launch == nil || launch.DaemonID == "" || !validCwd(launch.Cwd) {
		return false
	}
	if launch.Agent.Brief == "" || len(launch.Agent.Brief) > protocol.MaxTerminalBrief {
		return false
	}
	for _, dir := range launch.Agent.OtherDirs {
		if !validCwd(dir) {
			return false
		}
	}
	return true
}

// openAgent starts an agent session in a task tree and binds the socket to it
// once the daemon answers ready. The tree is resolved with the relay's lock
// released, since that reads the database; the socket's reader is the caller,
// so nothing else arrives from this browser meanwhile.
func (b *browserConn) openAgent(msg *protocol.BrowserTerminalMessage) bool {
	t := b.relay
	t.mu.Lock()
	switch {
	case b.closed:
		t.mu.Unlock()
		return false
	case b.terminal != nil:
		t.failLocked(b, protocol.TerminalErrBadRequest, "socket is already bound to a terminal")
	case msg.Cwd != "" || msg.Project != "":
		t.failLocked(b, protocol.TerminalErrBadRequest, "open takes a directory, a task tree or a project, not two of them")
	case !validAgentRef(msg.Agent):
		t.failLocked(b, protocol.TerminalErrBadRequest, "agent needs an issue_id and a tree_id")
	case !protocol.ValidTerminalSize(msg.Cols, msg.Rows):
		t.failLocked(b, protocol.TerminalErrBadRequest, badSizeMessage)
	case t.agent == nil:
		t.failLocked(b, protocol.TerminalErrInternal, "agent terminals are not configured")
	}
	closed, resolver := b.closed, t.agent
	t.mu.Unlock()
	if closed {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.openTimeout)
	defer cancel()
	launch, refusal := resolver.ResolveAgent(ctx, b.userID, *msg.Agent)

	t.mu.Lock()
	defer t.mu.Unlock()
	if b.closed {
		return false
	}
	if refusal == nil && !validLaunch(launch) {
		refusal = &protocol.TerminalError{Code: protocol.TerminalErrInternal, Message: "the task could not be prepared for the daemon"}
	}
	if refusal != nil {
		code := refusal.Code
		if !protocol.KnownTerminalError(code) {
			code = protocol.TerminalErrInternal
		}
		t.failLocked(b, code, refusal.Message)
		return false
	}
	// The session runs on the daemon that built the tree, which need not be
	// the user's own: whether that one is connected is the next question.
	d := t.daemonByIDLocked(b.userID, launch.DaemonID)
	if d == nil {
		t.failLocked(b, protocol.TerminalErrRuntimeOffline, "the daemon that holds this tree is not connected")
		return false
	}
	t.requestLocked(d, &pendingRequest{browser: b, kind: pendingOpen}, protocol.DaemonTerminalMessage{
		Type:  protocol.TerminalMsgOpen,
		Cwd:   launch.Cwd,
		Cols:  msg.Cols,
		Rows:  msg.Rows,
		Agent: &launch.Agent,
	})
	return !b.closed
}

// daemonByIDLocked returns the connected terminal link of the named daemon,
// whoever owns it. When the same daemon is connected under several owners, the
// user's own link is used. The caller holds t.mu.
func (t *Terminal) daemonByIDLocked(userID, daemonID string) *daemonConn {
	var found *daemonConn
	for key, d := range t.daemons {
		if key.daemonID != daemonID {
			continue
		}
		if key.userID == userID {
			return d
		}
		if found == nil {
			found = d
		}
	}
	return found
}
