package terminal

import "github.com/multica-ai/multica/server/pkg/protocol"

// Command is the process a terminal runs instead of the login shell.
type Command struct {
	// Argv is the program and its arguments; argv[0] is the executable.
	Argv []string
	// Env is the process's whole environment. Nil means the daemon's.
	Env []string
	// Cleanup releases what the command needed, such as a settings file. It
	// runs once, after the terminal has exited; a command that is never
	// started, because the terminal could not be, is cleaned up right away.
	Cleanup func()
}

// Launcher turns the agent part of an open into the process to run in cwd,
// the start tree's path.
type Launcher interface {
	Launch(cwd string, agent *protocol.DaemonTerminalAgent) (*Command, *Refusal)
}
