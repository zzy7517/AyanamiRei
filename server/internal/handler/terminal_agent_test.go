package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/terminal"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// handlerFlagsOn makes the handler read only the flags listed, all on.
func handlerFlagsOn(t *testing.T, keys ...string) {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	for _, key := range keys {
		provider.Set(key, featureflag.Rule{Default: true})
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })
}

// agentTrees is a task with two trees, of the fixture's two projects, both
// not built yet.
type agentTrees struct{ issueID, first, second string }

func newAgentTrees(t *testing.T, f projectScopeFixture) agentTrees {
	t.Helper()
	trees := agentTrees{issueID: dbfx.Issue(t, "Agent terminal task", testutil.Cols{"description": "Build the thing."})}
	scopes := putIssueScope(t, trees.issueID, scopeTestPair{f.projectID, "main"}, scopeTestPair{f.otherProjectID, "main"})
	trees.first, trees.second = scopes.Items[0].Tree.ID, scopes.Items[1].Tree.ID
	return trees
}

func TestResolveAgentLaunchesFromAReadyTree(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	handlerFlagsOn(t, featureflags.TerminalV1, featureflags.TaskSessionsV1)
	f := newProjectScopeFixture(t)
	tr := newAgentTrees(t, f)
	reportTaskTreeReady(t, tr.issueID, tr.first, projectScopeDaemonID, "/srv/tasks/agent/first", "task/first")
	reportTaskTreeReady(t, tr.issueID, tr.second, projectScopeDaemonID, "/srv/tasks/agent/second", "task/second")
	ctx := context.Background()

	launch, refusal := testHandler.ResolveAgent(ctx, testUserID, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.first})
	if refusal != nil {
		t.Fatalf("resolve refused: %+v", refusal)
	}
	agent := launch.Agent
	if launch.DaemonID != projectScopeDaemonID || launch.Cwd != "/srv/tasks/agent/first" ||
		agent.IssueID != tr.issueID || agent.StartTreeID != tr.first ||
		len(agent.OtherDirs) != 1 || agent.OtherDirs[0] != "/srv/tasks/agent/second" {
		t.Fatalf("launch = %+v", launch)
	}
	// The brief is the task, its trees with the one the session starts in
	// marked, and the delivery target.
	for _, want := range []string{
		"Agent terminal task", "Build the thing.", "Project scope project", "Project scope other project",
		"/srv/tasks/agent/first", "/srv/tasks/agent/second", "task/first", "main",
	} {
		if !strings.Contains(agent.Brief, want) {
			t.Fatalf("the brief lacks %q:\n%s", want, agent.Brief)
		}
	}
	if first, second := strings.Index(agent.Brief, "task/first"), strings.Index(agent.Brief, "task/second"); first > second {
		t.Fatalf("the start tree is not listed first:\n%s", agent.Brief)
	}

	// Starting in the other tree swaps their roles.
	launch, refusal = testHandler.ResolveAgent(ctx, testUserID, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.second})
	if refusal != nil {
		t.Fatalf("resolve refused: %+v", refusal)
	}
	if launch.Cwd != "/srv/tasks/agent/second" || launch.Agent.StartTreeID != tr.second ||
		len(launch.Agent.OtherDirs) != 1 || launch.Agent.OtherDirs[0] != "/srv/tasks/agent/first" {
		t.Fatalf("launch from the second tree = %+v", launch)
	}

	// A tree that is not built is no other directory to open.
	reportTreeState(t, tr.issueID, tr.first, map[string]any{"daemon_id": projectScopeDaemonID, "state": "failed", "error": "no disk"}).Want(200)
	launch, refusal = testHandler.ResolveAgent(ctx, testUserID, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.second})
	if refusal != nil || len(launch.Agent.OtherDirs) != 0 {
		t.Fatalf("launch with a failed sibling = %+v, %v; want no other directories", launch, refusal)
	}
}

func TestResolveAgentRefusesWhatTheUserMayNotOpen(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	handlerFlagsOn(t, featureflags.TerminalV1, featureflags.TaskSessionsV1)
	f := newProjectScopeFixture(t)
	tr := newAgentTrees(t, f)
	reportTaskTreeReady(t, tr.issueID, tr.first, projectScopeDaemonID, "/srv/tasks/agent/first", "task/first")
	other := newAgentTrees(t, f)
	reportTaskTreeReady(t, other.issueID, other.first, projectScopeDaemonID, "/srv/tasks/agent/other", "task/other")
	outsider := dbfx.User(t, "Agent outsider", "agent-outsider-"+uuid.NewString()+"@example.test")
	ctx := context.Background()

	for _, tc := range []struct {
		name, user, code string
		ref              protocol.BrowserAgentRef
	}{
		{"a tree that is not built", testUserID, protocol.TerminalErrTreeNotReady, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.second}},
		{"a tree of another task", testUserID, protocol.TerminalErrNotFound, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: other.first}},
		{"an unknown tree", testUserID, protocol.TerminalErrNotFound, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: uuid.NewString()}},
		{"an unknown issue", testUserID, protocol.TerminalErrNotFound, protocol.BrowserAgentRef{IssueID: uuid.NewString(), TreeID: tr.first}},
		{"an identifier for an issue", testUserID, protocol.TerminalErrNotFound, protocol.BrowserAgentRef{IssueID: "MUL-1", TreeID: tr.first}},
		{"a non-member", outsider, protocol.TerminalErrNotFound, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.first}},
	} {
		launch, refusal := testHandler.ResolveAgent(ctx, tc.user, tc.ref)
		if launch != nil || refusal == nil || refusal.Code != tc.code {
			t.Fatalf("%s: resolve = %+v, %+v; want a %s refusal", tc.name, launch, refusal, tc.code)
		}
	}

	// Either flag off ends it, before anything is read.
	for _, keys := range [][]string{{featureflags.TaskSessionsV1}, {featureflags.TerminalV1}} {
		handlerFlagsOn(t, keys...)
		launch, refusal := testHandler.ResolveAgent(ctx, testUserID, protocol.BrowserAgentRef{IssueID: tr.issueID, TreeID: tr.first})
		if launch != nil || refusal == nil || refusal.Code != protocol.TerminalErrFlagDisabled {
			t.Fatalf("with only %v on: resolve = %+v, %+v; want a flag_disabled refusal", keys, launch, refusal)
		}
	}
	_ = terminal.AgentLaunch{}
}
