package agenthooks

import (
	"path/filepath"
	"testing"
	"time"
)

func wantTurnEnd(t *testing.T, m *Manager, want TurnEnd) {
	t.Helper()
	select {
	case got := <-m.TurnEnds():
		if got != want {
			t.Fatalf("turn end = %+v, want %+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no turn end arrived; want %+v", want)
	}
}

func wantNoTurnEnd(t *testing.T, m *Manager) {
	t.Helper()
	select {
	case got := <-m.TurnEnds():
		t.Fatalf("an unexpected turn end arrived: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// The receiver tells turn ends apart by directory, and the issue of a turn
// end is the one the agent terminal in that directory claimed.
func TestManagerTurnEndsReportTheMappedIssue(t *testing.T) {
	m := newTestManager(t, Options{})
	token := "Bearer " + mintToken(t, m)
	tree := filepath.Join(t.TempDir(), "tree")
	other := filepath.Join(t.TempDir(), "other")

	// Two terminals run in the tree, one elsewhere.
	m.MapIssue(tree, "issue-1")
	m.MapIssue(tree, "issue-1")
	m.MapIssue(other, "issue-2")

	postHook(t, m, token, hookJSON(t, eventStopFailure, "s1", map[string]any{"cwd": tree, "error": "api_error"}))
	wantTurnEnd(t, m, TurnEnd{IssueID: "issue-1", Cwd: tree, SessionID: "s1"})
	// Neither a prompt nor the Stop that follows a Stop hook's block ends a turn.
	postHook(t, m, token, hookJSON(t, eventUserPromptSubmit, "s2", map[string]any{"cwd": other}))
	postHook(t, m, token, hookJSON(t, eventStop, "s2", map[string]any{"cwd": other, "stop_hook_active": true}))
	wantNoTurnEnd(t, m)
	postHook(t, m, token, hookJSON(t, eventStop, "s2", map[string]any{"cwd": other}))
	wantTurnEnd(t, m, TurnEnd{IssueID: "issue-2", Cwd: other, SessionID: "s2"})

	// One terminal of the tree ending keeps the mapping for the other.
	m.UnmapIssue(tree)
	postHook(t, m, token, hookJSON(t, eventStop, "s3", map[string]any{"cwd": tree}))
	wantTurnEnd(t, m, TurnEnd{IssueID: "issue-1", Cwd: tree, SessionID: "s3"})
	m.UnmapIssue(tree)
	postHook(t, m, token, hookJSON(t, eventStop, "s4", map[string]any{"cwd": tree}))
	wantTurnEnd(t, m, TurnEnd{Cwd: tree, SessionID: "s4"})
}
