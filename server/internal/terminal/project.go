package terminal

import (
	"context"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectLaunch is what a project open resolved to: the folder the shell
// starts in, and the daemon whose machine holds it.
type ProjectLaunch struct {
	// DaemonID is the daemon the folder was registered on.
	DaemonID string
	// Cwd is the folder's absolute path, as written on that daemon's machine.
	// It is not held to the server's path rules: the daemon need not run the
	// server's operating system, and it is the daemon that checks the folder.
	Cwd string
}

// ProjectResolver loads the project folder a project open names, for the user
// who asked. Exactly one of its results is non-nil: a refusal reaches the browser
// with its code, which has to be one of the contract's.
//
// It is not asked about the default project: where that folder is, is the
// daemon's to know.
type ProjectResolver interface {
	ResolveProject(ctx context.Context, userID, resourceID string) (*ProjectLaunch, *protocol.TerminalError)
}

// validProjectID reports whether an open's project names a folder at all.
// What the id means is the resolver's to decide.
func validProjectID(project string) bool {
	const maxIDLen = 128
	return project != "" && len(project) <= maxIDLen && !strings.ContainsRune(project, 0)
}

// validProjectLaunch reports whether the daemon can be given launch as it is.
func validProjectLaunch(launch *ProjectLaunch) bool {
	return launch != nil && launch.DaemonID != "" && launch.Cwd != "" &&
		!strings.ContainsRune(launch.DaemonID, 0) && !strings.ContainsRune(launch.Cwd, 0)
}

// openProject starts a shell in a project folder and binds the socket to it
// once the daemon answers ready.
//
// The default project is asked of a daemon at once, like a directory the
// browser named, since only the daemon knows where its folder is. A project's
// registered folder is resolved first, with the relay's lock released, since
// that reads the database; the socket's reader is the caller, so nothing else
// arrives from this browser meanwhile.
func (b *browserConn) openProject(msg *protocol.BrowserTerminalMessage) bool {
	t := b.relay
	t.mu.Lock()
	switch {
	case b.closed:
	case b.terminal != nil:
		t.failLocked(b, protocol.TerminalErrBadRequest, "socket is already bound to a terminal")
	case msg.Cwd != "" || msg.Agent != nil:
		t.failLocked(b, protocol.TerminalErrBadRequest, "open takes a directory, a task tree or a project, not two of them")
	case !validProjectID(msg.Project):
		t.failLocked(b, protocol.TerminalErrBadRequest, "project must name the default project or a project folder")
	case !protocol.ValidTerminalSize(msg.Cols, msg.Rows):
		t.failLocked(b, protocol.TerminalErrBadRequest, badSizeMessage)
	case msg.Project == protocol.TerminalProjectDefault:
		t.requestOpenLocked(b, msg.DaemonID, protocol.DaemonTerminalMessage{
			Type:    protocol.TerminalMsgOpen,
			Project: protocol.DaemonTerminalProjectDefault,
			Cols:    msg.Cols,
			Rows:    msg.Rows,
		})
	case t.project == nil:
		t.failLocked(b, protocol.TerminalErrInternal, "project terminals are not configured")
	}
	isDefault, closed, resolver := msg.Project == protocol.TerminalProjectDefault, b.closed, t.project
	t.mu.Unlock()
	if closed || isDefault {
		return !closed
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.openTimeout)
	defer cancel()
	launch, refusal := resolver.ResolveProject(ctx, b.userID, msg.Project)

	t.mu.Lock()
	defer t.mu.Unlock()
	if b.closed {
		return false
	}
	if refusal == nil && !validProjectLaunch(launch) {
		refusal = &protocol.TerminalError{Code: protocol.TerminalErrInternal, Message: "the project folder could not be prepared for the daemon"}
	}
	if refusal != nil {
		code := refusal.Code
		if !protocol.KnownTerminalError(code) {
			code = protocol.TerminalErrInternal
		}
		t.failLocked(b, code, refusal.Message)
		return false
	}
	// The shell runs on the daemon the folder was registered on, which need
	// not be connected: whether it is, is the next question. Another of the
	// user's daemons cannot stand in, since the folder is not on its machine.
	d := t.daemonByIDLocked(b.userID, launch.DaemonID)
	if d == nil {
		t.failLocked(b, protocol.TerminalErrDaemonUnavailable, "the daemon that holds this project folder is not connected")
		return false
	}
	t.requestLocked(d, &pendingRequest{browser: b, kind: pendingOpen}, protocol.DaemonTerminalMessage{
		Type:    protocol.TerminalMsgOpen,
		Project: protocol.DaemonTerminalProjectFolder,
		Cwd:     launch.Cwd,
		Cols:    msg.Cols,
		Rows:    msg.Rows,
	})
	return !b.closed
}
