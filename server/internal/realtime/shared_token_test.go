package realtime

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
)

const sharedTestSecret = "open-sesame-not-a-jwt"

// useSharedToken configures the pre-shared token for one test and removes it
// again afterwards.
func useSharedToken(t *testing.T, userID string) {
	t.Helper()
	tok := auth.NewSharedToken(sharedTestSecret, "owner@example.com")
	if userID != "" {
		tok.BindUser(userID)
	}
	auth.SetSharedToken(tok)
	t.Cleanup(func() { auth.SetSharedToken(nil) })
}

func TestAuthenticateTokenAcceptsSharedToken(t *testing.T) {
	useSharedToken(t, "user-9")

	// No PAT resolver: the shared token must not depend on the PAT path.
	uid, errMsg := authenticateToken(sharedTestSecret, nil, context.Background())
	if errMsg != "" || uid != "user-9" {
		t.Fatalf("authenticateToken(secret) = %q, %q; want user-9 and no error", uid, errMsg)
	}

	// Checked before the prefix branches: an operator may pick a secret that
	// starts like a PAT, and it must still resolve as the shared token.
	mulShaped := auth.NewSharedToken("mul_open-sesame", "owner@example.com")
	mulShaped.BindUser("user-9")
	auth.SetSharedToken(mulShaped)
	uid, errMsg = authenticateToken("mul_open-sesame", staticPATResolver{"mul_open-sesame": "someone-else"}, context.Background())
	if errMsg != "" || uid != "user-9" {
		t.Fatalf("authenticateToken(mul_-shaped secret) = %q, %q; want user-9 and no error", uid, errMsg)
	}
}

func TestAuthenticateTokenRefusesSharedTokenItShould(t *testing.T) {
	tests := []struct {
		name    string
		bound   string
		token   string
		wantErr string
	}{
		{name: "wrong token", bound: "user-9", token: "open-sesame", wantErr: `{"error":"invalid token"}`},
		{name: "unbound token", token: sharedTestSecret, wantErr: `{"error":"invalid token"}`},
		{
			name: "temporarily disabled account", bound: "1d542296-17c6-484a-9914-dcee589be116",
			token: sharedTestSecret, wantErr: `{"error":"account disabled"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useSharedToken(t, tc.bound)
			uid, errMsg := authenticateToken(tc.token, nil, context.Background())
			if errMsg != tc.wantErr || uid != "" {
				t.Fatalf("authenticateToken() = %q, %q; want \"\" and %s", uid, errMsg, tc.wantErr)
			}
		})
	}
}

func TestAuthenticateTokenSharedTokenUnsetLeavesBehaviourUnchanged(t *testing.T) {
	auth.SetSharedToken(nil)
	uid, errMsg := authenticateToken(sharedTestSecret, nil, context.Background())
	if errMsg != `{"error":"invalid token"}` || uid != "" {
		t.Fatalf("unset, the secret is just an invalid JWT; got %q, %q", uid, errMsg)
	}
}
