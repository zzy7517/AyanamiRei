package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A runtime delete takes the user agents bound to it off the runtime instead
// of deleting them: they survive unbound, and the autopilots assigned to
// those agents pause. An active agent still blocks the delete.

// seedIsolatedRuntime creates a fresh runtime in the shared test workspace
// (so the seeded test user is owner/admin and passes canEditRuntime), and
// returns its UUID. The runtime is auto-cleaned via t.Cleanup; tests that
// successfully drive DeleteAgentRuntime through to the end will have already
// deleted it, in which case the cleanup is a no-op.
func seedIsolatedRuntime(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'isolated_test', 'online', 'isolated test runtime', '{}'::jsonb, now())
		RETURNING id
	`, testWorkspaceID, name).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime %q: %v", name, err)
	}
	t.Cleanup(func() {
		// Best-effort cascading cleanup; ignore errors because the handler
		// may have already removed the row in the happy path.
		testPool.Exec(ctx, `DELETE FROM agent WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

// seedAgentOnRuntime creates an agent on the given runtime. If archived is
// true the row is created with archived_at = now().
func seedAgentOnRuntime(t *testing.T, runtimeID, name string, archived bool) string {
	t.Helper()
	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, name, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent %q: %v", name, err)
	}
	if archived {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET archived_at = now(), archived_by = $1 WHERE id = $2`,
			testUserID, agentID,
		); err != nil {
			t.Fatalf("archive agent %q: %v", name, err)
		}
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})
	return agentID
}

func agentExists(t *testing.T, agentID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent WHERE id = $1`, agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count agent %s: %v", agentID, err)
	}
	return count == 1
}

func runtimeExists(t *testing.T, runtimeID string) bool {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID,
	).Scan(&count); err != nil {
		t.Fatalf("count runtime %s: %v", runtimeID, err)
	}
	return count == 1
}

// agentRuntimeBound reports whether the agent still points at a runtime.
func agentRuntimeBound(t *testing.T, agentID string) bool {
	t.Helper()
	var bound bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT runtime_id IS NOT NULL FROM agent WHERE id = $1`, agentID,
	).Scan(&bound); err != nil {
		t.Fatalf("read runtime_id for agent %s: %v", agentID, err)
	}
	return bound
}

// seedActiveAutopilot inserts an active run_only autopilot assigned to agentID
// and registers its cleanup.
func seedActiveAutopilot(t *testing.T, title string, agentID string) string {
	t.Helper()
	var autopilotID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO autopilot (
			workspace_id, title, assignee_id, created_by_type, created_by_id, status, execution_mode
		)
		VALUES ($1, $2, $3, 'member', $4, 'active', 'run_only')
		RETURNING id
	`, testWorkspaceID, title, agentID, testUserID).Scan(&autopilotID); err != nil {
		t.Fatalf("seed autopilot %q: %v", title, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id = $1`, autopilotID)
	})
	return autopilotID
}

// readAutopilotStatus reads an autopilot's status and pause reason, "" when it has none.
func readAutopilotStatus(t *testing.T, autopilotID string) (status, pauseReason string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		`SELECT status, COALESCE(pause_reason, '') FROM autopilot WHERE id = $1`, autopilotID,
	).Scan(&status, &pauseReason); err != nil {
		t.Fatalf("read autopilot %s: %v", autopilotID, err)
	}
	return status, pauseReason
}

// deleteRuntimeForTest drives DeleteAgentRuntime as the seeded test user.
func deleteRuntimeForTest(t *testing.T, runtimeID string) *httptest.ResponseRecorder {
	t.Helper()
	req := withURLParam(newRequest("DELETE", "/api/runtimes/"+runtimeID, nil), "runtimeId", runtimeID)
	w := httptest.NewRecorder()
	testHandler.DeleteAgentRuntime(w, req)
	return w
}

// TestDeleteAgentRuntime_UnbindsAndKeepsArchivedAgents confirms the common
// case: an archived agent survives as an unbound agent and the runtime is gone.
func TestDeleteAgentRuntime_UnbindsAndKeepsArchivedAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Archived Agent")
	archivedAgent := seedAgentOnRuntime(t, runtimeID, "Archived Agent", true)

	if w := deleteRuntimeForTest(t, runtimeID); w.Code != http.StatusOK {
		t.Fatalf("DeleteAgentRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !agentExists(t, archivedAgent) {
		t.Errorf("archived agent must survive its runtime as an unbound agent")
	}
	if agentRuntimeBound(t, archivedAgent) {
		t.Errorf("surviving agent must be unbound (runtime_id IS NULL)")
	}
	if runtimeExists(t, runtimeID) {
		t.Errorf("runtime should have been deleted")
	}
}

// TestDeleteAgentRuntime_PausesOnlyAutopilotsOfItsOwnAgents: the teardown
// pauses the active autopilots assigned to the agents it unbinds, and leaves
// an autopilot assigned to an agent of another runtime alone.
func TestDeleteAgentRuntime_PausesOnlyAutopilotsOfItsOwnAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Archived Autopilot Agent")
	archivedAgent := seedAgentOnRuntime(t, runtimeID, "Archived Agent With Autopilot", true)
	otherRuntimeID := seedIsolatedRuntime(t, "Other Runtime With Autopilot Agent")
	otherAgent := seedAgentOnRuntime(t, otherRuntimeID, "Other Agent With Autopilot", false)
	autopilotID := seedActiveAutopilot(t, "runtime pause", archivedAgent)
	otherAutopilotID := seedActiveAutopilot(t, "unrelated autopilot", otherAgent)

	if w := deleteRuntimeForTest(t, runtimeID); w.Code != http.StatusOK {
		t.Fatalf("DeleteAgentRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !agentExists(t, archivedAgent) {
		t.Errorf("archived agent must survive the runtime delete")
	}
	if !runtimeExists(t, otherRuntimeID) {
		t.Errorf("another runtime must survive the delete")
	}
	if runtimeExists(t, runtimeID) {
		t.Errorf("runtime should have been deleted")
	}
	if status, pauseReason := readAutopilotStatus(t, autopilotID); status != "paused" || pauseReason != string(ReasonAgentRuntimeRequired) {
		t.Fatalf("autopilot of the unbound agent = (%q, %q), want (paused, agent_runtime_required)", status, pauseReason)
	}
	if status, _ := readAutopilotStatus(t, otherAutopilotID); status != "active" {
		t.Fatalf("autopilot of an agent on another runtime = %q, want active", status)
	}
}

// TestDeleteAgentRuntime_StillBlockedByActiveAgents preserves the existing 409
// contract: a runtime with at least one ACTIVE agent must still refuse the
// strict delete, so the user always sees and confirms which agents are about to
// lose their runtime before anything happens.
func TestDeleteAgentRuntime_StillBlockedByActiveAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := seedIsolatedRuntime(t, "Runtime With Active Agent")
	activeAgent := seedAgentOnRuntime(t, runtimeID, "Active Agent Blocking Delete", false)

	if w := deleteRuntimeForTest(t, runtimeID); w.Code != http.StatusConflict {
		t.Fatalf("DeleteAgentRuntime: expected 409 active-agent guard, got %d: %s", w.Code, w.Body.String())
	}

	if !agentExists(t, activeAgent) {
		t.Errorf("active agent must NOT have been touched by a refused runtime delete")
	}
	if !agentRuntimeBound(t, activeAgent) {
		t.Errorf("active agent must still be bound after a refused runtime delete")
	}
	if !runtimeExists(t, runtimeID) {
		t.Errorf("runtime must NOT have been deleted by a refused delete")
	}
}
