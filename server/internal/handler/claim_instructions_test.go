package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// claimInstructionsFixture wires a runtime, an agent on it and an issue
// assigned to that agent: the IDs needed to enqueue the agent's issue tasks
// against that runtime.
type claimInstructionsFixture struct {
	RuntimeID string
	AgentID   string
	IssueID   string
}

func newClaimInstructionsFixture(t *testing.T, ctx context.Context, name string) claimInstructionsFixture {
	t.Helper()

	runtimeID := createClaimReclaimRuntime(t, ctx, name+" runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, name+" agent")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentID); err != nil {
		t.Fatalf("assign issue to agent: %v", err)
	}
	return claimInstructionsFixture{RuntimeID: runtimeID, AgentID: agentID, IssueID: issueID}
}

// enqueueClaimTask queues an issue task for the fixture's agent and returns
// its id.
func enqueueClaimTask(t *testing.T, ctx context.Context, fx claimInstructionsFixture) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, fx.AgentID, fx.RuntimeID, fx.IssueID).Scan(&taskID); err != nil {
		t.Fatalf("enqueue claim task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	return taskID
}

// claimRawForTest claims the next queued task for runtimeID and returns the
// raw claim body.
func claimRawForTest(t *testing.T, runtimeID string) string {
	t.Helper()

	w := httptest.NewRecorder()
	req := newOwnerDaemonRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
	req = withURLParam(req, "runtimeId", runtimeID)

	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// A run works on the step the issue was at when it was queued. Once someone
// else moves the issue, the claim tells the agent to leave the status alone
// and offers it no status move; a run on the current step, bound or not, is
// still told how to finish it.
func TestClaim_TaskWhoseStepMovedGetsNoStatusMoves(t *testing.T) {
	seedTestCatalog(t)
	for _, bound := range []string{"waiting_for_merge", "in_progress", ""} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("bound=%s/legacy=%v", bound, legacy), func(t *testing.T) {
				fx := newClaimInstructionsFixture(t, t.Context(), "Workflow claim")
				projectID := deliveryProject(t, "Workflow claim project", fx.AgentID)
				dbfx.Exec(t, `UPDATE issue SET project_id=$2,status='in_progress' WHERE id=$1`, fx.IssueID, projectID)
				cols := testutil.Cols{"runtime_id": fx.RuntimeID, "issue_id": fx.IssueID}
				if bound != "" {
					cols["workflow_step"] = bound
				}
				taskID := dbfx.Task(t, fx.AgentID, cols)
				req := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+fx.RuntimeID+"/tasks/claim", nil), "runtimeId", fx.RuntimeID)
				if !legacy {
					req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityWorkflowStatusRulesV1)
				}
				var resp struct {
					Task *AgentTaskResponse `json:"task"`
				}
				testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&resp)
				if resp.Task == nil || resp.Task.ID != taskID || resp.Task.Agent == nil || resp.Task.ProjectWorkflow == nil {
					t.Fatalf("missing workflow claim: %+v", resp.Task)
				}
				brief := resp.Task.ProjectWorkflow.Brief
				if bound == "waiting_for_merge" {
					if !strings.Contains(brief, "Do not change the issue's status in this run") {
						t.Fatalf("a stale run's brief does not refuse status changes:\n%s", brief)
					}
					if strings.Contains(brief, "Step done") || strings.Contains(brief, "multica issue status") {
						t.Fatalf("a stale run was offered a status move:\n%s", brief)
					}
				} else if !strings.Contains(brief, "Step done") {
					t.Fatalf("a current or unbound run lost its wrap-up instruction:\n%s", brief)
				}
			})
		}
	}
}

// TestClaim_DocsEnabledFollowsFlag pins docs_enabled on the claim: the daemon
// writes `multica doc` into the agent brief only when the server says so.
func TestClaim_DocsEnabledFollowsFlag(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("docs_v1=%v", enabled), func(t *testing.T) {
			withFeatureFlag(t, testHandler, featureflags.DocsV1, enabled)
			ctx := context.Background()
			fx := newClaimInstructionsFixture(t, ctx, fmt.Sprintf("Docs flag %v", enabled))
			enqueueClaimTask(t, ctx, fx)

			raw := claimRawForTest(t, fx.RuntimeID)
			if got := strings.Contains(raw, `"docs_enabled":true`); got != enabled {
				t.Fatalf("docs_enabled in claim = %v, want %v: %s", got, enabled, raw)
			}
		})
	}
}
