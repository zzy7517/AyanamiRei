package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/auth"
)

const sharedTestSecret = "open-sesame-not-a-jwt"

// useSharedToken configures the pre-shared token for one test, bound to the
// integration account, and removes it again afterwards.
func useSharedToken(t *testing.T) {
	t.Helper()
	tok := auth.NewSharedToken(sharedTestSecret, integrationTestEmail)
	tok.BindUser(testUserID)
	auth.SetSharedToken(tok)
	t.Cleanup(func() { auth.SetSharedToken(nil) })
}

// getWith calls the running router with token as the bearer credential.
func getWith(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, testServer.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s with the shared token: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// wantStatus fails unless the response has the status, printing the body: the
// body is where the router says which of its rejections fired.
func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode == want {
		return
	}
	body, _ := io.ReadAll(resp.Body)
	t.Fatalf("GET %s: expected %d, got %d: %s", resp.Request.URL.Path, want, resp.StatusCode, body)
}

// wsURL is the realtime endpoint's address for the integration workspace.
func wsURL() string {
	return "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws?workspace_id=" + testWorkspaceID
}

// authWithSharedToken opens /ws and sends token in the first frame, returning
// the first frame the server answers.
func authWithSharedToken(t *testing.T, token string) string {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(), nil)
	if err != nil {
		t.Fatalf("dial /ws: %v", err)
	}
	defer conn.Close()

	frame, err := json.Marshal(map[string]any{
		"type":    "auth",
		"payload": map[string]string{"token": token},
	})
	if err != nil {
		t.Fatalf("encode auth frame: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatalf("send auth frame: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, reply, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read the server's answer to the auth frame: %v", err)
	}
	return string(reply)
}

func TestSharedTokenThroughRouter(t *testing.T) {
	useSharedToken(t)

	// /api/**: the token is the bound account.
	resp := getWith(t, "/api/me", sharedTestSecret)
	wantStatus(t, resp, http.StatusOK)
	var me struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	readJSON(t, resp, &me)
	if me.ID != testUserID || me.Email != integrationTestEmail {
		t.Fatalf("GET /api/me with the shared token returned %s <%s>; want the bound account %s <%s>",
			me.ID, me.Email, testUserID, integrationTestEmail)
	}

	// The daemon routes accept a personal access token; they accept this too.
	wantStatus(t, getWith(t, "/api/daemon/workspaces", sharedTestSecret), http.StatusOK)

	// /ws authenticates the first frame with it.
	if reply := authWithSharedToken(t, sharedTestSecret); !strings.Contains(reply, "auth_ack") {
		t.Fatalf("/ws answered %s to the shared token; want auth_ack", reply)
	}

	// So does the daemon's WebSocket, which authenticates the upgrade request.
	daemonWS := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/daemon/ws"
	header := http.Header{"Authorization": {"Bearer " + sharedTestSecret}}
	conn, dialResp, err := websocket.DefaultDialer.Dial(daemonWS, header)
	if dialResp != nil && dialResp.Body != nil {
		dialResp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial /api/daemon/ws with the shared token: %v", err)
	}
	conn.Close()
}

func TestWrongSharedTokenThroughRouter(t *testing.T) {
	useSharedToken(t)

	wantStatus(t, getWith(t, "/api/me", "open-sesame"), http.StatusUnauthorized)
	wantStatus(t, getWith(t, "/api/daemon/workspaces", "open-sesame"), http.StatusUnauthorized)
	if reply := authWithSharedToken(t, "open-sesame"); !strings.Contains(reply, `"error":"invalid token"`) {
		t.Fatalf("/ws answered %s to a wrong token; want the invalid-token error frame", reply)
	}
}

func TestSharedTokenUnsetThroughRouter(t *testing.T) {
	auth.SetSharedToken(nil)

	// Unset, the secret is just an invalid credential everywhere.
	wantStatus(t, getWith(t, "/api/me", sharedTestSecret), http.StatusUnauthorized)
	wantStatus(t, getWith(t, "/api/daemon/workspaces", sharedTestSecret), http.StatusUnauthorized)
	if reply := authWithSharedToken(t, sharedTestSecret); !strings.Contains(reply, `"error":"invalid token"`) {
		t.Fatalf("/ws answered %s while no shared token is configured; want the invalid-token error frame", reply)
	}
}
