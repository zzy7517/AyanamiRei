package terminal

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeProjectResolver stands in for the handler's resolver and remembers what
// it was asked.
type fakeProjectResolver struct {
	launch  *ProjectLaunch
	refusal *protocol.TerminalError
	calls   int
	user    string
	id      string
}

func (f *fakeProjectResolver) ResolveProject(_ context.Context, userID, resourceID string) (*ProjectLaunch, *protocol.TerminalError) {
	f.calls++
	f.user, f.id = userID, resourceID
	return f.launch, f.refusal
}

func projectOpenMsg(project, daemonID string) protocol.BrowserTerminalMessage {
	return protocol.BrowserTerminalMessage{
		Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Project: project, DaemonID: daemonID,
	}
}

// The shell runs on the daemon the folder was registered on, and the terminal
// belongs to the user who opened it. The path is the daemon's to read, so the
// relay passes it on as it was registered.
func TestProjectOpenRunsOnTheDaemonTheFolderWasRegisteredOn(t *testing.T) {
	r := &fakeProjectResolver{launch: &ProjectLaunch{DaemonID: "holder", Cwd: `C:\code\app`}}
	h := startHarness(t, true, func(o *Options) { o.Project = r })
	d := h.daemon("another-member", "holder")
	b := h.browser("u1")

	b.send(projectOpenMsg("folder-1", ""))
	req := d.expect(protocol.TerminalMsgOpen)
	if req.Cwd != `C:\code\app` || req.Cols != 120 || req.Rows != 40 || req.RequestID == "" ||
		req.Project != protocol.DaemonTerminalProjectFolder || req.Agent != nil {
		t.Fatalf("daemon was asked %+v", req)
	}
	if r.calls != 1 || r.user != "u1" || r.id != "folder-1" {
		t.Fatalf("resolver was asked %d times, last for %q, %q", r.calls, r.user, r.id)
	}
	d.send(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t-1", PID: 4242, Cols: 120, Rows: 40,
	})
	wantControl(t, b.nextBrowser(), map[string]any{
		"type": protocol.TerminalMsgReady, "terminalId": "t-1", "pid": 4242, "cols": 120, "rows": 40,
	})

	// Its opener can come back to it; the daemon's owner cannot.
	attach(t, h.browser("u1"), d, "t-1")
	stranger := h.browser("another-member")
	stranger.send(protocol.BrowserTerminalMessage{Type: protocol.TerminalMsgAttach, TerminalID: "t-1"})
	stranger.wantError(protocol.TerminalErrNotFound)
}

// The default project is the daemon's to place, so nothing is looked up: the
// browser names the project, and the daemon is chosen as for any other open.
func TestDefaultProjectOpenNeedsNoResolver(t *testing.T) {
	r := &fakeProjectResolver{}
	h := startHarness(t, true, func(o *Options) { o.Project = r })
	d := h.daemon("u1", "daemon-a")
	b := h.browser("u1")

	b.send(projectOpenMsg(protocol.TerminalProjectDefault, "daemon-a"))
	req := d.expect(protocol.TerminalMsgOpen)
	if req.Project != protocol.DaemonTerminalProjectDefault || req.Cwd != "" || req.Agent != nil ||
		req.Cols != 120 || req.Rows != 40 || req.RequestID == "" {
		t.Fatalf("daemon was asked %+v", req)
	}
	d.send(protocol.DaemonTerminalMessage{
		Type: protocol.TerminalMsgReady, RequestID: req.RequestID, TerminalID: "t-1", PID: 4242, Cols: 120, Rows: 40,
	})
	wantControl(t, b.nextBrowser(), map[string]any{"type": protocol.TerminalMsgReady, "terminalId": "t-1"})

	// With two daemons of the user's connected, the open has to name one.
	other := h.daemon("u1", "daemon-b")
	undecided := h.browser("u1")
	undecided.send(projectOpenMsg(protocol.TerminalProjectDefault, ""))
	undecided.wantError(protocol.TerminalErrDaemonUnavailable)
	named := h.browser("u1")
	named.send(projectOpenMsg(protocol.TerminalProjectDefault, "daemon-b"))
	again := other.expect(protocol.TerminalMsgOpen)
	if again.Project != protocol.DaemonTerminalProjectDefault || again.Cwd != "" {
		t.Fatalf("the named daemon was asked %+v", again)
	}

	// A relay with no resolver at all still opens the default project.
	bare := startHarness(t, true)
	bareDaemon := bare.daemon("u1", "daemon-a")
	bareBrowser := bare.browser("u1")
	bareBrowser.send(projectOpenMsg(protocol.TerminalProjectDefault, "daemon-a"))
	bareDaemon.expect(protocol.TerminalMsgOpen)

	if r.calls != 0 {
		t.Fatalf("the resolver was asked %d times about the default project", r.calls)
	}
}

func TestProjectOpenRefusals(t *testing.T) {
	launch := &ProjectLaunch{DaemonID: "holder", Cwd: "/home/me/code/app"}

	t.Run("the machine the folder is on is not connected", func(t *testing.T) {
		h := startHarness(t, true, func(o *Options) { o.Project = &fakeProjectResolver{launch: launch} })
		// The folder is not on this daemon's machine, so it is not asked.
		d := h.daemon("u1", "daemon-a")
		b := h.browser("u1")
		b.send(projectOpenMsg("folder-1", ""))
		if got := b.wantError(protocol.TerminalErrDaemonUnavailable); got["message"] == "" {
			t.Fatal("the error carries no message")
		}
		openTerminal(t, h.browser("u1"), d, "daemon-a", "t1")
	})

	t.Run("what the resolver refuses reaches the browser", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			resolver *fakeProjectResolver
			code     string
			// message is checked when it is not empty.
			message string
		}{
			{"the folder is gone",
				&fakeProjectResolver{refusal: &protocol.TerminalError{Code: protocol.TerminalErrProjectNotFound, Message: "gone"}},
				protocol.TerminalErrProjectNotFound, "gone"},
			{"a code outside the contract",
				&fakeProjectResolver{refusal: &protocol.TerminalError{Code: "whatever the resolver says", Message: "why"}},
				protocol.TerminalErrInternal, "why"},
			{"nothing usable came back", &fakeProjectResolver{}, protocol.TerminalErrInternal, ""},
			{"no daemon named", &fakeProjectResolver{launch: &ProjectLaunch{Cwd: "/home/me/code/app"}}, protocol.TerminalErrInternal, ""},
			{"no folder named", &fakeProjectResolver{launch: &ProjectLaunch{DaemonID: "holder"}}, protocol.TerminalErrInternal, ""},
		} {
			h := startHarness(t, true, func(o *Options) { o.Project = tc.resolver })
			h.daemon("u1", "daemon-a")
			b := h.browser("u1")
			b.send(projectOpenMsg("folder-1", ""))
			if got := b.wantError(tc.code); tc.message != "" && got["message"] != tc.message {
				t.Fatalf("%s: error = %v, want the message %q", tc.name, got, tc.message)
			}
		}
	})

	t.Run("without touching the resolver", func(t *testing.T) {
		r := &fakeProjectResolver{launch: launch}
		h := startHarness(t, true, func(o *Options) { o.Project = r })
		for _, msg := range []protocol.BrowserTerminalMessage{
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Project: "folder-1", Cwd: "/srv/app"},
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Project: "folder-1", Agent: &protocol.BrowserAgentRef{IssueID: "i-1", TreeID: "t-1"}},
			{Type: protocol.TerminalMsgOpen, Cols: 0, Rows: 40, Project: "folder-1"},
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 0, Project: protocol.TerminalProjectDefault},
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Project: protocol.TerminalProjectDefault, Cwd: "/srv/app"},
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Project: strings.Repeat("x", 129)},
		} {
			b := h.browser("u1")
			b.send(msg)
			b.wantError(protocol.TerminalErrBadRequest)
		}
		if r.calls != 0 {
			t.Fatalf("the resolver was asked %d times", r.calls)
		}
	})

	t.Run("with no resolver configured", func(t *testing.T) {
		h := startHarness(t, true)
		d := h.daemon("u1", "daemon-a")
		b := h.browser("u1")
		b.send(projectOpenMsg("folder-1", "daemon-a"))
		b.wantError(protocol.TerminalErrInternal)
		// The daemon was asked nothing: the next thing it hears is the open that follows.
		openTerminal(t, h.browser("u1"), d, "daemon-a", "t1")
	})
}
