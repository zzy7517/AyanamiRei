package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// involvesFixture seeds, for a single test, the data needed to exercise the
// `involves_user_id` filter: an agent the user owns, one someone else owns, and
// a mirror of the owned agent in a second workspace so the cross-workspace
// negative test can prove subquery-level isolation.
type involvesFixture struct {
	// All IDs are in the primary handler-test workspace unless noted otherwise.
	userID  string // the "me" user the filter is keyed on (== testUserID)
	otherID string // a different user in the same workspace

	ownedAgentID string // agent.owner_id = userID — matches
	otherAgentID string // agent.owner_id = otherID — must NOT match

	// Other workspace, mirror objects — used by ExcludesOtherWorkspace* tests
	otherWsID    string
	otherWsAgent string // owned by userID but in other workspace
}

func setupInvolvesFixture(t *testing.T) *involvesFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	fx := &involvesFixture{userID: testUserID}

	// --- second user inside the primary workspace ---
	var otherUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Involves Other User", fmt.Sprintf("involves-other-%d@multica.ai", suffix)).Scan(&otherUserID); err != nil {
		t.Fatalf("create other user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, otherUserID) })
	fx.otherID = otherUserID
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, testWorkspaceID, otherUserID); err != nil {
		t.Fatalf("create other member: %v", err)
	}

	runtimeID := handlerTestRuntimeID(t)

	// --- agents in primary workspace ---
	fx.ownedAgentID = insertAgent(t, ctx, testWorkspaceID, runtimeID, fx.userID,
		fmt.Sprintf("Involves Owned Agent %d", suffix))
	fx.otherAgentID = insertAgent(t, ctx, testWorkspaceID, runtimeID, fx.otherID,
		fmt.Sprintf("Involves Other Agent %d", suffix))

	// --- second workspace, mirroring the owned agent for cross-ws negatives ---
	var otherWsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'OTH')
		RETURNING id
	`, fmt.Sprintf("InvolvesOtherWs-%d", suffix), fmt.Sprintf("involves-other-ws-%d", suffix)).Scan(&otherWsID); err != nil {
		t.Fatalf("create other workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, otherWsID) })
	fx.otherWsID = otherWsID

	// Membership in other workspace (so the user could legitimately be assigned
	// there too — exercises whether subquery workspace_id clause filters it out).
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
	`, otherWsID, fx.userID); err != nil {
		t.Fatalf("create other-ws member: %v", err)
	}

	var otherRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at
		) VALUES ($1, NULL, $2, 'cloud', 'other_ws_runtime', 'online', $3, '{}'::jsonb, now())
		RETURNING id
	`, otherWsID, fmt.Sprintf("OtherWs Runtime %d", suffix), "other-ws-runtime").Scan(&otherRuntimeID); err != nil {
		t.Fatalf("create other runtime: %v", err)
	}

	fx.otherWsAgent = insertAgent(t, ctx, otherWsID, otherRuntimeID, fx.userID,
		fmt.Sprintf("OtherWs Owned Agent %d", suffix))

	return fx
}

func insertAgent(t *testing.T, ctx context.Context, workspaceID, runtimeID, ownerID, name string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, workspaceID, name, runtimeID, ownerID).Scan(&id); err != nil {
		t.Fatalf("create agent %q: %v", name, err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id) })
	return id
}

// insertIssueTo creates an issue in the given workspace assigned to the given
// (assigneeType, assigneeID) pair and returns its UUID. Issue rows are
// best-effort-cleaned up by the test.
func insertIssueTo(t *testing.T, ctx context.Context, workspaceID, title, assigneeType, assigneeID string) string {
	t.Helper()
	var number int32
	if err := testPool.QueryRow(ctx, `
		UPDATE workspace
		SET issue_counter = GREATEST(
			issue_counter,
			(SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)
		) + 1
		WHERE id = $1
		RETURNING issue_counter
	`, workspaceID).Scan(&number); err != nil {
		t.Fatalf("next issue number: %v", err)
	}
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			assignee_type, assignee_id, creator_type, creator_id,
			position, number
		)
		VALUES ($1, $2, NULL, 'todo', 'none', $3, $4, 'member', $5, 0, $6)
		RETURNING id
	`, workspaceID, title, assigneeType, assigneeID, testUserID, number).Scan(&id); err != nil {
		t.Fatalf("create issue %q: %v", title, err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	return id
}

// listIssuesInvolves runs ListIssues with `involves_user_id` set to userID
// (against testWorkspaceID) and returns the resulting issue IDs.
func listIssuesInvolves(t *testing.T, userID string) []string {
	t.Helper()
	path := fmt.Sprintf("/api/issues?workspace_id=%s&involves_user_id=%s&limit=500",
		testWorkspaceID, userID)
	w := httptest.NewRecorder()
	testHandler.ListIssues(w, newRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Issues []IssueResponse `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	ids := make([]string, 0, len(resp.Issues))
	for _, iss := range resp.Issues {
		ids = append(ids, iss.ID)
	}
	return ids
}

// listGroupedIssuesInvolves runs ListGroupedIssues with `involves_user_id`
// set to userID and returns the flattened set of issue IDs across all groups.
func listGroupedIssuesInvolves(t *testing.T, userID string) []string {
	t.Helper()
	path := fmt.Sprintf(
		"/api/issues/grouped?workspace_id=%s&group_by=assignee&statuses=todo&involves_user_id=%s&limit=100",
		testWorkspaceID, userID)
	w := httptest.NewRecorder()
	testHandler.ListGroupedIssues(w, newRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListGroupedIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp GroupedIssuesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode grouped response: %v", err)
	}
	ids := []string{}
	for _, g := range resp.Groups {
		for _, iss := range g.Issues {
			ids = append(ids, iss.ID)
		}
	}
	return ids
}

func containsIssueID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// ---- positive branches ----

func TestListIssues_InvolvesUserID_MatchesOwnedAgentAssignee(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	wantID := insertIssueTo(t, ctx, testWorkspaceID,
		"issue assigned to my owned agent", "agent", fx.ownedAgentID)
	if got := listIssuesInvolves(t, fx.userID); !containsIssueID(got, wantID) {
		t.Fatalf("owned-agent assignee not surfaced (want %s, got %v)", wantID, got)
	}
}

// ---- the critical negative: tab 3 must be disjoint from tab 1 ----

// Nails the semantics: `involves_user_id` MUST NOT surface issues whose
// assignee is the user themself (member type). Direct member assignment is
// the meaning of `assignee_id` (tab 1 "Assigned to me"); the two tabs must
// produce disjoint result sets. If anyone adds an
// `(assignee_type='member' AND assignee_id=involves_user_id)` branch back in,
// this test fails.
func TestListIssues_InvolvesUserID_ExcludesDirectMemberAssignee(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	issueID := insertIssueTo(t, ctx, testWorkspaceID,
		"tab 3 must NOT surface member-direct assignment", "member", fx.userID)
	if got := listIssuesInvolves(t, fx.userID); containsIssueID(got, issueID) {
		t.Fatalf("tab 3 semantics violated: involves_user_id surfaced a member-direct assignee issue (id=%s); that belongs to tab 1. Full result: %v",
			issueID, got)
	}
}

// Same negative on the grouped (dynamic SQL) path — the dynamic builder is a
// separate code path from sqlc, so it gets its own regression.
func TestListGroupedIssues_InvolvesUserID_ExcludesDirectMemberAssignee(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	issueID := insertIssueTo(t, ctx, testWorkspaceID,
		"grouped tab 3 must NOT surface member-direct assignment", "member", fx.userID)
	if got := listGroupedIssuesInvolves(t, fx.userID); containsIssueID(got, issueID) {
		t.Fatalf("grouped tab 3 semantics violated: involves_user_id surfaced a member-direct assignee issue (id=%s); full result: %v",
			issueID, got)
	}
}

// ---- workspace isolation negatives — each subquery must clamp workspace_id ----

func TestListIssues_InvolvesUserID_ExcludesOtherWorkspaceAgent(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	// Issue lives in the *primary* workspace but is assigned to an agent UUID
	// that only exists in the OTHER workspace and is owned by our user. If the
	// agent subquery is missing `a.workspace_id = $1`, this match would leak.
	issueID := insertIssueTo(t, ctx, testWorkspaceID,
		"cross-ws agent assignee must not leak", "agent", fx.otherWsAgent)
	if got := listIssuesInvolves(t, fx.userID); containsIssueID(got, issueID) {
		t.Fatalf("workspace isolation violated: cross-workspace agent surfaced (id=%s); full result: %v",
			issueID, got)
	}
}

// ---- combo + boundary ----

// involves_user_id and creator_id must AND together — combining narrowing
// filters should never widen the result.
func TestListIssues_InvolvesUserID_CombinesWithCreatorID(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	// Issue with creator = otherID: involves matches (owned agent) but creator
	// filter must exclude it.
	exclude := insertIssueTo(t, ctx, testWorkspaceID,
		"involves matches but creator does not", "agent", fx.ownedAgentID)
	// Patch the creator to otherID directly.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET creator_id = $1 WHERE id = $2`, fx.otherID, exclude); err != nil {
		t.Fatalf("patch creator: %v", err)
	}

	path := fmt.Sprintf("/api/issues?workspace_id=%s&involves_user_id=%s&creator_id=%s&limit=500",
		testWorkspaceID, fx.userID, fx.userID)
	w := httptest.NewRecorder()
	testHandler.ListIssues(w, newRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssues: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Issues []IssueResponse `json:"issues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	got := make([]string, 0, len(resp.Issues))
	for _, iss := range resp.Issues {
		got = append(got, iss.ID)
	}
	if containsIssueID(got, exclude) {
		t.Fatalf("combined filter widened result: issue %s with non-matching creator surfaced; full result: %v",
			exclude, got)
	}
}

func TestListIssues_InvolvesUserID_InvalidUUIDReturns400(t *testing.T) {
	path := fmt.Sprintf("/api/issues?workspace_id=%s&involves_user_id=not-a-uuid", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.ListIssues(w, newRequest("GET", path, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid UUID, got %d: %s", w.Code, w.Body.String())
	}
}

// The grouped path has its own SQL builder, so it needs its own positive and
// its own exclusion of an agent the user does not own.
func TestListGroupedIssues_InvolvesUserID_MatchesOwnedAgentAssignee(t *testing.T) {
	ctx := context.Background()
	fx := setupInvolvesFixture(t)
	wantID := insertIssueTo(t, ctx, testWorkspaceID,
		"grouped: my owned agent", "agent", fx.ownedAgentID)
	mineID := insertIssueTo(t, ctx, testWorkspaceID,
		"grouped: assigned to me", "member", fx.userID)
	theirID := insertIssueTo(t, ctx, testWorkspaceID,
		"grouped: someone else's agent", "agent", fx.otherAgentID)
	got := listGroupedIssuesInvolves(t, fx.userID)
	if !containsIssueID(got, wantID) {
		t.Fatalf("grouped owned-agent assignee not surfaced (want %s, got %v)", wantID, got)
	}
	if containsIssueID(got, mineID) {
		t.Fatalf("grouped member-direct assignee should not surface (id=%s, got=%v)", mineID, got)
	}
	if containsIssueID(got, theirID) {
		t.Fatalf("grouped agent owned by someone else should not surface (id=%s, got=%v)", theirID, got)
	}
}
