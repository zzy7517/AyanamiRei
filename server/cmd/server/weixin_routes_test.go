package main

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestWeixinRoutesRequireWorkspaceAdminForQRAndDisconnect(t *testing.T) {
	base := "/api/workspaces/" + testWorkspaceID + "/weixin"
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, base + "/installations", http.StatusOK},
		{http.MethodPost, base + "/logins?agent_id=11111111-1111-4111-8111-111111111111", http.StatusForbidden},
		{http.MethodPost, base + "/logins/11111111-1111-4111-8111-111111111111/poll", http.StatusForbidden},
		{http.MethodDelete, base + "/logins/11111111-1111-4111-8111-111111111111", http.StatusForbidden},
		{http.MethodDelete, base + "/installations/11111111-1111-4111-8111-111111111111", http.StatusForbidden},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := testutil.JSONRequest(tc.method, tc.path, map[string]string{})
			testutil.WithHeaders(req, "X-Workspace-ID", testWorkspaceID)
			testutil.Call(t, testServer.Config.Handler.ServeHTTP, req).Want(tc.want)
		})
	}
}
