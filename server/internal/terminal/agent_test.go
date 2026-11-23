package terminal

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeResolver stands in for the handler's resolver and remembers what it was asked.
type fakeResolver struct {
	launch  *AgentLaunch
	refusal *protocol.TerminalError
	calls   int
	user    string
	ref     protocol.BrowserAgentRef
}

func (f *fakeResolver) ResolveAgent(_ context.Context, userID string, ref protocol.BrowserAgentRef) (*AgentLaunch, *protocol.TerminalError) {
	f.calls++
	f.user, f.ref = userID, ref
	return f.launch, f.refusal
}

func agentOpenMsg(issueID, treeID string) protocol.BrowserTerminalMessage {
	return protocol.BrowserTerminalMessage{
		Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40,
		Agent: &protocol.BrowserAgentRef{IssueID: issueID, TreeID: treeID},
	}
}

// The session runs on the daemon that built the tree, whoever owns that
// daemon, and the terminal belongs to the user who opened it.
func TestAgentOpenRunsOnTheDaemonThatBuiltTheTree(t *testing.T) {
	r := &fakeResolver{launch: &AgentLaunch{
		DaemonID: "builder", Cwd: "/srv/tree",
		Agent: protocol.DaemonTerminalAgent{IssueID: "i-1", StartTreeID: "t-1", OtherDirs: []string{"/srv/other"}, Brief: "MUL-1 do it"},
	}}
	h := startHarness(t, true, func(o *Options) { o.Agent = r })
	d := h.daemon("another-member", "builder")
	b := h.browser("u1")

	b.send(agentOpenMsg("i-1", "t-1"))
	req := d.expect(protocol.TerminalMsgOpen)
	if req.Cwd != "/srv/tree" || req.Cols != 120 || req.Rows != 40 || req.RequestID == "" ||
		req.Agent == nil || req.Agent.IssueID != "i-1" || req.Agent.StartTreeID != "t-1" ||
		len(req.Agent.OtherDirs) != 1 || req.Agent.OtherDirs[0] != "/srv/other" || req.Agent.Brief != "MUL-1 do it" {
		t.Fatalf("daemon was asked %+v", req)
	}
	if r.calls != 1 || r.user != "u1" || r.ref != (protocol.BrowserAgentRef{IssueID: "i-1", TreeID: "t-1"}) {
		t.Fatalf("resolver was asked %d times, last for %q, %+v", r.calls, r.user, r.ref)
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

func TestAgentOpenRefusals(t *testing.T) {
	launch := &AgentLaunch{DaemonID: "builder", Cwd: "/srv/tree", Agent: protocol.DaemonTerminalAgent{IssueID: "i-1", Brief: "brief"}}

	t.Run("the daemon is not connected", func(t *testing.T) {
		h := startHarness(t, true, func(o *Options) { o.Agent = &fakeResolver{launch: launch} })
		b := h.browser("u1")
		b.send(agentOpenMsg("i-1", "t-1"))
		if got := b.wantError(protocol.TerminalErrRuntimeOffline); got["message"] == "" {
			t.Fatal("the error carries no message")
		}
	})

	t.Run("what the resolver refuses reaches the browser", func(t *testing.T) {
		for _, tc := range []struct {
			refusal *protocol.TerminalError
			code    string
		}{
			{&protocol.TerminalError{Code: protocol.TerminalErrTreeNotReady, Message: "still building"}, protocol.TerminalErrTreeNotReady},
			{&protocol.TerminalError{Code: "whatever the resolver says"}, protocol.TerminalErrInternal},
			{nil, protocol.TerminalErrInternal}, // nothing usable came back
		} {
			h := startHarness(t, true, func(o *Options) {
				o.Agent = &fakeResolver{refusal: tc.refusal}
			})
			b := h.browser("u1")
			b.send(agentOpenMsg("i-1", "t-1"))
			b.wantError(tc.code)
		}
	})

	t.Run("without touching the resolver", func(t *testing.T) {
		r := &fakeResolver{launch: launch}
		h := startHarness(t, true, func(o *Options) { o.Agent = r })
		for _, msg := range []protocol.BrowserTerminalMessage{
			{Type: protocol.TerminalMsgOpen, Cwd: "/srv/app", Cols: 120, Rows: 40, Agent: &protocol.BrowserAgentRef{IssueID: "i-1", TreeID: "t-1"}},
			{Type: protocol.TerminalMsgOpen, Cols: 120, Rows: 40, Agent: &protocol.BrowserAgentRef{IssueID: "i-1"}},
			{Type: protocol.TerminalMsgOpen, Cols: 0, Rows: 40, Agent: &protocol.BrowserAgentRef{IssueID: "i-1", TreeID: "t-1"}},
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
		b := h.browser("u1")
		b.send(agentOpenMsg("i-1", "t-1"))
		b.wantError(protocol.TerminalErrInternal)
	})
}
