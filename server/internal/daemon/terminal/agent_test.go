//go:build linux

package terminal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeLauncher stands in for the agent session launcher: it runs what it is
// told and reports when its command was cleaned up.
type fakeLauncher struct {
	argv    []string
	env     []string
	refusal *Refusal
	cwd     string
	agent   *protocol.DaemonTerminalAgent
	cleaned chan struct{}
}

func newFakeLauncher(argv, env []string) *fakeLauncher {
	return &fakeLauncher{argv: argv, env: env, cleaned: make(chan struct{}, 4)}
}

func (f *fakeLauncher) Launch(cwd string, agent *protocol.DaemonTerminalAgent) (*Command, *Refusal) {
	f.cwd, f.agent = cwd, agent
	if f.refusal != nil {
		return nil, f.refusal
	}
	return &Command{Argv: f.argv, Env: f.env, Cleanup: func() { f.cleaned <- struct{}{} }}, nil
}

func (f *fakeLauncher) awaitCleaned(t *testing.T, what string) {
	t.Helper()
	select {
	case <-f.cleaned:
	case <-time.After(shellDeadline):
		t.Fatalf("the command was not cleaned up after %s", what)
	}
}

func agentOpen(requestID, cwd string) *protocol.DaemonTerminalMessage {
	return &protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgOpen, RequestID: requestID, Cwd: cwd, Cols: 80, Rows: 24,
		Agent: &protocol.DaemonTerminalAgent{IssueID: "i-1", StartTreeID: "t-1", OtherDirs: []string{"/srv/other"}, Brief: "MUL-1 do it"},
	}
}

// An agent open runs what the launcher returns, in the tree's directory and
// with the launcher's environment. The terminal root does not apply to it.
func TestLinkRunsWhatTheLauncherReturns(t *testing.T) {
	script := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'agent-up %s\\n' \"$AGENT_TOKEN\"\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeLauncher([]string{script}, []string{"AGENT_TOKEN=from-env", "PATH=/usr/bin:/bin"})
	root := t.TempDir()
	cfg := newTestConfig(root, t.TempDir(), "/bin/sh")
	cfg.Launcher = f
	l := NewLink(cfg)
	t.Cleanup(l.Close)
	tree := t.TempDir() // next to the root, not inside it

	l.Handle(agentOpen("r1", tree))
	answer := nextControl(t, l)
	if answer.Type != protocol.TerminalMsgReady || answer.RequestID != "r1" ||
		answer.TerminalID == "" || answer.PID <= 0 || answer.Cols != 80 || answer.Rows != 24 {
		t.Fatalf("agent open answered with %+v", answer)
	}
	if f.cwd != tree || f.agent == nil || f.agent.IssueID != "i-1" || f.agent.StartTreeID != "t-1" || f.agent.Brief != "MUL-1 do it" {
		t.Fatalf("launcher was asked for %q, %+v", f.cwd, f.agent)
	}
	if out := outputUntil(t, l, answer.TerminalID, "agent-up from-env"); out == "" {
		t.Fatal("no output from the agent")
	}
	select {
	case <-f.cleaned:
		t.Fatal("the command was cleaned up while its terminal still runs")
	default:
	}

	// Ending the terminal releases what the launcher prepared, once.
	l.Handle(&protocol.DaemonTerminalMessage{Type: protocol.TerminalMsgClose, TerminalID: answer.TerminalID})
	f.awaitCleaned(t, "a close")
	if exit := nextControl(t, l); exit.Type != protocol.TerminalMsgExit || exit.TerminalID != answer.TerminalID {
		t.Fatalf("after the close the link sent %+v", exit)
	}
	select {
	case <-f.cleaned:
		t.Fatal("the command was cleaned up twice")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestLinkRefusesAnAgentOpenItCannotRun(t *testing.T) {
	tree := t.TempDir()

	t.Run("without a launcher", func(t *testing.T) {
		l := newTestLink(t, t.TempDir())
		l.Handle(agentOpen("r1", tree))
		if answer := nextControl(t, l); answer.Type != protocol.TerminalMsgError || answer.RequestID != "r1" || answer.Code != protocol.TerminalErrInternal {
			t.Fatalf("answered with %+v", answer)
		}
	})

	t.Run("the launcher refuses", func(t *testing.T) {
		f := newFakeLauncher(nil, nil)
		f.refusal = refuse(protocol.TerminalErrInternal, "no hook receiver")
		cfg := newTestConfig(t.TempDir(), t.TempDir(), "/bin/sh")
		cfg.Launcher = f
		l := NewLink(cfg)
		t.Cleanup(l.Close)
		l.Handle(agentOpen("r2", tree))
		if answer := nextControl(t, l); answer.Code != protocol.TerminalErrInternal || answer.Message != "no hook receiver" {
			t.Fatalf("answered with %+v", answer)
		}
	})

	t.Run("the tree is not on this machine", func(t *testing.T) {
		// Never started, so what was prepared for it is released at once.
		f := newFakeLauncher([]string{"/bin/sh"}, nil)
		cfg := newTestConfig(t.TempDir(), t.TempDir(), "/bin/sh")
		cfg.Launcher = f
		l := NewLink(cfg)
		t.Cleanup(l.Close)
		l.Handle(agentOpen("r3", filepath.Join(tree, "gone")))
		if answer := nextControl(t, l); answer.Code != protocol.TerminalErrPathNotAllowed {
			t.Fatalf("answered with %+v", answer)
		}
		f.awaitCleaned(t, "a refused open")
	})

	t.Run("the open is malformed", func(t *testing.T) {
		f := newFakeLauncher([]string{"/bin/sh"}, nil)
		cfg := newTestConfig(t.TempDir(), t.TempDir(), "/bin/sh")
		cfg.Launcher = f
		l := NewLink(cfg)
		t.Cleanup(l.Close)
		for _, mutate := range []func(*protocol.DaemonTerminalMessage){
			func(m *protocol.DaemonTerminalMessage) {
				m.Agent.Brief = string(make([]byte, protocol.MaxTerminalBrief+1))
			},
			func(m *protocol.DaemonTerminalMessage) { m.Agent.OtherDirs = []string{"relative/path"} },
			func(m *protocol.DaemonTerminalMessage) { m.Agent.IssueID = "" },
		} {
			msg := agentOpen("r4", tree)
			mutate(msg)
			l.Handle(msg)
			if answer := nextControl(t, l); answer.Code != protocol.TerminalErrBadRequest {
				t.Fatalf("answered with %+v", answer)
			}
		}
		if f.cwd != "" {
			t.Fatal("the launcher was asked to launch a malformed open")
		}
	})
}
