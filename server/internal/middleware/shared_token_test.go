package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
)

const (
	sharedTestSecret = "open-sesame-not-a-jwt"
	disabledTestUser = "514492f7-b30f-4147-bd33-c0e8ce5d6d4f"
)

// useSharedToken configures the pre-shared token for one test, bound to
// userID unless that is empty, and removes it again afterwards.
func useSharedToken(t *testing.T, userID string) {
	t.Helper()
	tok := auth.NewSharedToken(sharedTestSecret, "owner@example.com")
	if userID != "" {
		tok.BindUser(userID)
	}
	auth.SetSharedToken(tok)
	t.Cleanup(func() { auth.SetSharedToken(nil) })
}

func bearerRequest(method, target, token string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// A client that holds the shared token knows it is a credential like any
	// other, so it must not be able to stamp an actor source on top of it.
	req.Header.Set("X-Actor-Source", "member")
	return req
}

func TestAuth_SharedTokenIsTheBoundUser(t *testing.T) {
	useSharedToken(t, "user-42")

	var gotUser, gotActorSource string
	var markedAsPAT bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User-ID")
		gotActorSource = r.Header.Get("X-Actor-Source")
		markedAsPAT = IsPersonalAccessToken(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	Auth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/me", sharedTestSecret))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected the request to pass, got %d: %s", w.Code, w.Body.String())
	}
	if gotUser != "user-42" {
		t.Errorf("X-User-ID = %q; want the bound user", gotUser)
	}
	if gotActorSource != "" {
		t.Errorf("X-Actor-Source = %q; want the client's value dropped, as on the PAT path", gotActorSource)
	}
	if !markedAsPAT {
		t.Error("the shared token must be marked as a personal access token for handlers that tell one apart")
	}
}

func TestAuth_SharedTokenIsRefusedWhenItShouldBe(t *testing.T) {
	tests := []struct {
		name   string
		bound  string
		status int
	}{
		// No account is bound yet (startup still preparing it, or the email
		// variable missing): the secret itself is refused.
		{name: "unbound", status: http.StatusUnauthorized},
		{name: "temporarily disabled account", bound: disabledTestUser, status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useSharedToken(t, tc.bound)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("next handler should not be called")
			})
			w := httptest.NewRecorder()
			Auth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/me", sharedTestSecret))
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestAuth_WrongSharedTokenIsUnauthorized(t *testing.T) {
	useSharedToken(t, "user-42")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})
	for _, token := range []string{
		"open-sesame", sharedTestSecret + "x", " " + sharedTestSecret, "mul_" + sharedTestSecret, "mat_" + sharedTestSecret,
	} {
		w := httptest.NewRecorder()
		Auth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/me", token))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: expected 401, got %d: %s", token, w.Code, w.Body.String())
		}
	}
}

func TestAuth_SharedTokenUnsetLeavesBehaviourUnchanged(t *testing.T) {
	auth.SetSharedToken(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})
	// The same string a configured server would accept is, unset, just an
	// invalid JWT.
	w := httptest.NewRecorder()
	Auth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/me", sharedTestSecret))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body != `{"error":"invalid token"}`+"\n" {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestDaemonAuth_SharedTokenIsTheBoundUser(t *testing.T) {
	useSharedToken(t, "user-7")

	var gotUser, gotPath string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User-ID")
		gotPath = DaemonAuthPathFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	DaemonAuth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/daemon/workspaces", sharedTestSecret))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected the request to pass, got %d: %s", w.Code, w.Body.String())
	}
	if gotUser != "user-7" {
		t.Errorf("X-User-ID = %q; want the bound user", gotUser)
	}
	if gotPath != DaemonAuthPathSharedToken {
		t.Errorf("auth path = %q; want %q", gotPath, DaemonAuthPathSharedToken)
	}
}

func TestDaemonAuth_RefusesSharedTokenItShould(t *testing.T) {
	tests := []struct {
		name   string
		bound  string
		token  string
		status int
	}{
		{name: "wrong token", bound: "user-7", token: "open-sesame", status: http.StatusUnauthorized},
		{name: "unbound token", token: sharedTestSecret, status: http.StatusUnauthorized},
		{name: "disabled account", bound: disabledTestUser, token: sharedTestSecret, status: http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useSharedToken(t, tc.bound)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("next handler should not be called")
			})
			w := httptest.NewRecorder()
			DaemonAuth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/daemon/workspaces", tc.token))
			if w.Code != tc.status {
				t.Fatalf("expected %d, got %d: %s", tc.status, w.Code, w.Body.String())
			}
		})
	}
}

func TestDaemonAuth_SharedTokenUnsetLeavesBehaviourUnchanged(t *testing.T) {
	auth.SetSharedToken(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	})
	w := httptest.NewRecorder()
	DaemonAuth(nil, nil, nil, nil)(next).ServeHTTP(w, bearerRequest(http.MethodGet, "/api/daemon/workspaces", sharedTestSecret))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}
