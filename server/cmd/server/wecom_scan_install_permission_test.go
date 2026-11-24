package main

import (
	"context"
	"net/http"
	"testing"
)

// Scan install creates and binds a bot. The deployment user is the workspace
// owner and reaches the handlers. Past the route, the handler answers 503
// when this server has no scan install configured, otherwise begin is 202 and
// status is 200.
func TestWecomScanInstallIsForWorkspaceAdmins(t *testing.T) {
	ctx := context.Background()

	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent WHERE workspace_id = $1 AND name = 'Integration Test Agent'
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load fixture agent: %v", err)
	}
	var sessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO wecom_install_session (request_key_hash, workspace_id, agent_id, initiator_user_id, status, error_reason)
		VALUES ('scan-permission', $1, $2, $3, 'error', 'expired')
		RETURNING id::text`, testWorkspaceID, agentID, testUserID).Scan(&sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM wecom_install_session WHERE id = $1`, sessionID)
	})

	base := testServer.URL + "/api/workspaces/" + testWorkspaceID + "/wecom/install/"
	do := func(t *testing.T, method, url string) int {
		t.Helper()
		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("Idempotency-Key", "scan-permission-"+method)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	begin := base + "begin?agent_id=" + agentID
	status := base + sessionID

	if got := do(t, http.MethodPost, begin); got != http.StatusAccepted && got != http.StatusServiceUnavailable {
		t.Errorf("begin = %d, want 202, or 503 with scan install off", got)
	}
	if got := do(t, http.MethodGet, status); got != http.StatusOK && got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 200, or 503 with scan install off", got)
	}
}
