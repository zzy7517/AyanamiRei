package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createPrivateAgentOwnedBy inserts a private agent owned by ownerID and
// returns its id. Mirrors privateAgentTestFixture's agent shape (visibility
// 'private', no explicit permission_mode so it keeps the private default the
// canInvokeAgent gate treats as owner-only), but lets a test stand up a SECOND
// private agent under the same human so the assignee and the worker can both
// be private and owned by the same originator.
func createPrivateAgentOwnedBy(t *testing.T, name, ownerID string) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb,
		        $3, 'private', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, name, handlerTestRuntimeID(t), ownerID).Scan(&agentID); err != nil {
		t.Fatalf("create private agent %q: %v", name, err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID) })
	return agentID
}

// TestAgentCreateOriginator_E2E_CreateAssignPrivateAgent_PrivateWorkerTriggered
// walks the exact line failure shape from MUL-4305 end to end, deliberately
// NOT re-testing the resolver in isolation but locking the real wiring between
// the handler create stamp, the create-time invoke gate, the assignee task's
// stored originator, the comment source-task stamp, and the private-worker
// invocation gate:
//
//	human H triggers agent A → A creates an issue via the ordinary
//	`issue create` path AND assigns it to a private agent L owned by H →
//	L's assignment run @-mentions a *second* private agent J (also owned by
//	H) → J must be triggered.
//
// Pre-fix, A's create left the issue unattributed, the assignment task stored a
// NULL originator, and L's mention of J failed canInvokeAgent — J silently got
// 0 tasks. This asserts the assignment task carries H and that J ends up with
// a queued task whose originator is the original human H.
func TestAgentCreateOriginator_E2E_CreateAssignPrivateAgent_PrivateWorkerTriggered(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Private worker J and the human originator H (J's owner).
	workerJID, ownerH, _ := privateAgentTestFixture(t)

	// Private assignee L, owned by the same human H.
	assigneeLID := createPrivateAgentOwnedBy(t, "mul4305-e2e-private-assignee", ownerH)

	// Creator agent A, running a task on behalf of the human H. resolveActor
	// validates the (A, task) pair; the handler then trusts X-Task-ID as A's
	// acting task and inherits H from it.
	creatorAID := createHandlerTestAgent(t, "mul4305-e2e-creator-agent", nil)
	var creatorTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, $2, $2)
		RETURNING id
	`, creatorAID, ownerH).Scan(&creatorTaskID); err != nil {
		t.Fatalf("create A's acting task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, creatorTaskID)
	})

	// Step 1: agent A creates an issue through the ordinary create path and
	// assigns it to the private agent L in the same call.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "MUL-4305 E2E agent-created + assigned to a private agent",
		"assignee_type": "agent",
		"assignee_id":   assigneeLID,
	})
	r.Header.Set("X-Agent-ID", creatorAID)
	r.Header.Set("X-Task-ID", creatorTaskID)
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	// The create must have stamped the issue back to A's acting task.
	var originType, originID string
	if err := testPool.QueryRow(ctx,
		`SELECT COALESCE(origin_type, ''), COALESCE(origin_id::text, '') FROM issue WHERE id = $1`, created.ID,
	).Scan(&originType, &originID); err != nil {
		t.Fatalf("load issue origin: %v", err)
	}
	if originType != "agent_create" || originID != creatorTaskID {
		t.Fatalf("issue origin = (%q,%q), want (agent_create,%s)", originType, originID, creatorTaskID)
	}

	// L's assignment task must have been enqueued carrying H.
	var assigneeTaskID, assigneeOriginator string
	if err := testPool.QueryRow(ctx, `
		SELECT id, COALESCE(originator_user_id::text, '')
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, created.ID, assigneeLID).Scan(&assigneeTaskID, &assigneeOriginator); err != nil {
		t.Fatalf("load assignee task (expected one enqueued on create): %v", err)
	}
	if assigneeOriginator != ownerH {
		t.Fatalf("assignee task originator = %q, want the original human H %q", assigneeOriginator, ownerH)
	}

	// Step 2: L, running its assignment task, posts a comment that @-mentions
	// the private worker J. The comment stamps source_task_id = L's task, so
	// the originator chain resolves to H.
	w = httptest.NewRecorder()
	r = newRequest("POST", "/api/issues/"+created.ID+"/comments", map[string]any{
		"content": "handing the private part to [@Worker](mention://agent/" + workerJID + ")",
	})
	r.Header.Set("X-Agent-ID", assigneeLID)
	r.Header.Set("X-Task-ID", assigneeTaskID)
	r = withURLParam(r, "id", created.ID)
	testHandler.CreateComment(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment (assignee mentions worker): expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Step 3: the private worker J must now have a queued task whose originator
	// is the original human H — the whole point of the fix.
	var queuedForHuman int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued' AND originator_user_id = $3
	`, created.ID, workerJID, ownerH).Scan(&queuedForHuman); err != nil {
		t.Fatalf("count worker tasks: %v", err)
	}
	if queuedForHuman == 0 {
		t.Fatalf("private worker got 0 queued tasks attributed to H; the A2A mention was denied (MUL-4305 regression)")
	}
}

// TestAgentCreateOriginator_E2E_UpdateAssignPrivateAgent_HandlerGateAdmitsIt
// locks the update-assign path, which the create test above does not
// exercise.
//
// Agent A (running for human H) creates an UNASSIGNED issue via the ordinary
// path, then assigns it to a private agent L owned by H via UpdateIssue. The
// assign clears the handler's invoke gate (validateAssigneePair) as the agent
// actor A because A's acting task carries H, and the run it enqueues is
// attributed through the issue's agent_create stamp, so L's task carries H.
func TestAgentCreateOriginator_E2E_UpdateAssignPrivateAgent_HandlerGateAdmitsIt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Reuse the private-agent fixture purely for a private agent + its human
	// owner H; the fixture agent here plays the assignee L.
	assigneeLID, ownerH, _ := privateAgentTestFixture(t)

	creatorAID := createHandlerTestAgent(t, "mul4305-e2e-update-creator", nil)
	var creatorTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, $2, $2)
		RETURNING id
	`, creatorAID, ownerH).Scan(&creatorTaskID); err != nil {
		t.Fatalf("create A's acting task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, creatorTaskID)
	})

	// Agent A creates an unassigned issue via the ordinary path.
	w := httptest.NewRecorder()
	r := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "MUL-4305 E2E unassigned then assigned to a private agent",
	})
	r.Header.Set("X-Agent-ID", creatorAID)
	r.Header.Set("X-Task-ID", creatorTaskID)
	testHandler.CreateIssue(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	// Agent A assigns the issue to the private agent L via UpdateIssue.
	w = httptest.NewRecorder()
	r = newRequest("PATCH", "/api/issues/"+created.ID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   assigneeLID,
	})
	r.Header.Set("X-Agent-ID", creatorAID)
	r.Header.Set("X-Task-ID", creatorTaskID)
	r = withURLParam(r, "id", created.ID)
	testHandler.UpdateIssue(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue (assign to a private agent): expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The gated handler path must have enqueued L's task carrying H.
	var assigneeCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND originator_user_id = $3
	`, created.ID, assigneeLID, ownerH).Scan(&assigneeCount); err != nil {
		t.Fatalf("count assignee tasks: %v", err)
	}
	if assigneeCount == 0 {
		t.Fatalf("private assignee got 0 tasks attributed to H after agent-triggered assign; the invoke gate denied it (MUL-4305 gate regression)")
	}
}
