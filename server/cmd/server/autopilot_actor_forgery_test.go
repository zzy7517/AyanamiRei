package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Autopilot writes are judged as the human the caller acts for, and for an
// agent caller that human is the run's ORIGINATOR. Only a server-issued mat_
// task token makes a request an agent's. Forged X-Agent-ID / X-Task-ID headers
// are stripped, so the deployment user stays themselves.
//
// The two halves use different tasks on purpose. Forgery is aimed at a run
// whose originator cannot write: if the headers were honored, the deployment
// user would be refused. The real token is bound to that same powerless
// member, but its task's originator can write, so admission can only come
// from the originator.
func TestAutopilotWritesIgnoreForgedAgentIdentity(t *testing.T) {
	fx := testutil.New(testPool, testWorkspaceID, testUserID)

	var agentID, runtimeID string
	fx.QueryRow(t, `SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND runtime_id IS NOT NULL LIMIT 1`, testWorkspaceID).
		Scan(&agentID, &runtimeID)

	email := fmt.Sprintf("autopilot-forger-%d@multica.test", time.Now().UnixNano())
	powerless := fx.User(t, "Autopilot Forger", email)
	fx.Member(t, testWorkspaceID, powerless, "member")

	task := func(originator string) string {
		return fx.Insert(t, "agent_task_queue", testutil.Cols{
			"agent_id": agentID, "runtime_id": runtimeID, "status": "running", "priority": 0,
			"originator_user_id": originator, "accountable_user_id": originator,
			"originator_source": "direct_human",
		})
	}
	forgedTaskID := task(powerless)
	tokenTaskID := task(testUserID)

	autopilotID := fx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "forged actor regression",
		"assignee_id": agentID, "execution_mode": "run_only",
		"status": "active", "created_by_type": "member", "created_by_id": testUserID,
	})
	fx.Cleanup(t, `DELETE FROM autopilot_rule_version WHERE autopilot_id = $1`, autopilotID)
	fx.Insert(t, "autopilot_trigger", testutil.Cols{
		"autopilot_id": autopilotID, "kind": "webhook", "enabled": true, "provider": "generic",
		"webhook_token":   fmt.Sprintf("tok_forgery_%d", time.Now().UnixNano()),
		"created_by_type": "member", "created_by_id": testUserID,
	})

	do := func(t *testing.T, method, path string, body any, decorate func(*http.Request)) *http.Response {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
		} else {
			reader = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, testServer.URL+path, reader)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		decorate(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		return resp
	}
	forged := func(req *http.Request) {
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", forgedTaskID)
	}

	t.Run("forged agent identity is judged as the deployment user", func(t *testing.T) {
		resp := do(t, "GET", "/api/autopilots/"+autopilotID+"?workspace_id="+testWorkspaceID, nil, forged)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("detail status = %d, want 200", resp.StatusCode)
		}
		var detail struct {
			Autopilot handler.AutopilotResponse          `json:"autopilot"`
			Triggers  []handler.AutopilotTriggerResponse `json:"triggers"`
		}
		readJSON(t, resp, &detail)
		if detail.Autopilot.CanWrite == nil || !*detail.Autopilot.CanWrite {
			t.Fatalf("can_write = %v, want true: forged headers must not replace the deployment user with the powerless originator", detail.Autopilot.CanWrite)
		}
		sawToken := false
		for _, tr := range detail.Triggers {
			if tr.WebhookToken != nil {
				sawToken = true
			}
		}
		if !sawToken {
			t.Fatal("forged headers hid the webhook credential from the deployment user, who can write this autopilot")
		}

		writeResp := do(t, "PATCH", "/api/autopilots/"+autopilotID+"?workspace_id="+testWorkspaceID,
			map[string]any{"status": "paused"}, forged)
		defer writeResp.Body.Close()
		if writeResp.StatusCode != http.StatusOK {
			t.Fatalf("update status = %d, want 200 — forged headers must not spend the originator's missing grant", writeResp.StatusCode)
		}
	})

	t.Run("a real task token is judged by its originator", func(t *testing.T) {
		token := mintAgentTaskToken(t, agentID, tokenTaskID, powerless)
		resp := do(t, "PATCH", "/api/autopilots/"+autopilotID+"?workspace_id="+testWorkspaceID,
			map[string]any{"status": "active"}, func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+token)
			})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update status = %d, want 200 — the ordering human may write this autopilot", resp.StatusCode)
		}
	})
}
