package handler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

const sharedTestSecret = "open-sesame-not-a-jwt"

// useSharedToken configures the pre-shared token for one test, bound to userID,
// and removes it again afterwards.
func useSharedToken(t *testing.T, userID string) {
	t.Helper()
	tok := auth.NewSharedToken(sharedTestSecret, handlerTestEmail)
	tok.BindUser(userID)
	auth.SetSharedToken(tok)
	t.Cleanup(func() { auth.SetSharedToken(nil) })
}

// getMeWith calls GET /api/me through the Auth middleware, the way /api/** is
// served, presenting token as the bearer credential.
func getMeWith(t *testing.T, token string) *testutil.Response {
	t.Helper()
	serve := middleware.Auth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.GetMe))
	req := testutil.WithHeaders(testutil.JSONRequest(http.MethodGet, "/api/me", nil), "Authorization", "Bearer "+token)
	return testutil.Call(t, serve.ServeHTTP, req)
}

func TestGetMeWithSharedTokenIsTheBoundUser(t *testing.T) {
	useSharedToken(t, testUserID)

	var me UserResponse
	getMeWith(t, sharedTestSecret).Want(http.StatusOK).JSON(&me)
	if me.ID != testUserID || me.Email != handlerTestEmail {
		t.Fatalf("GET /api/me with the shared token returned %s <%s>; want the bound account %s <%s>",
			me.ID, me.Email, testUserID, handlerTestEmail)
	}
}

func TestGetMeWithWrongSharedTokenIsUnauthorized(t *testing.T) {
	useSharedToken(t, testUserID)
	for _, token := range []string{"open-sesame", sharedTestSecret + "x", " " + sharedTestSecret} {
		getMeWith(t, token).Want(http.StatusUnauthorized)
	}
}

func TestGetMeWithSharedTokenUnsetLeavesBehaviourUnchanged(t *testing.T) {
	auth.SetSharedToken(nil)
	// Unset, the same string is just an invalid credential.
	getMeWith(t, sharedTestSecret).Want(http.StatusUnauthorized)
}

func TestRenewPATWithSharedTokenAnswersNeverExpires(t *testing.T) {
	useSharedToken(t, testUserID)

	w := httptest.NewRecorder()
	testHandler.RenewCurrentPersonalAccessToken(w, newRenewRequest(sharedTestSecret))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if resp := decodeRenewResponse(t, w); resp.Renewed || resp.ExpiresAt != "" {
		t.Fatalf("expected renewed=false with no expiry, got %+v", resp)
	}
}

func newSharedTokenEmail() string {
	return fmt.Sprintf("shared-token-%d@multica.ai", time.Now().UnixNano())
}

// removeSharedTokenAccount drops what EnsureSharedTokenUser opened for userID:
// the account and, through its membership, the first workspace. The schema has
// no cascading deletes, so each table is named, in dependency order.
func removeSharedTokenAccount(t *testing.T, userID string) {
	t.Helper()
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM issue_status WHERE workspace_id IN (SELECT workspace_id FROM member WHERE user_id = $1)`,
			`DELETE FROM workspace WHERE id IN (SELECT workspace_id FROM member WHERE user_id = $1)`,
			`DELETE FROM member WHERE user_id = $1`,
			`DELETE FROM "user" WHERE id = $1`,
		} {
			_, _ = testPool.Exec(context.Background(), stmt, userID)
		}
	})
}

func TestEnsureSharedTokenUserOpensTheAccountOnce(t *testing.T) {
	ctx := context.Background()
	email := newSharedTokenEmail()

	// The operator named this account explicitly: the switches that gate
	// everyone else's signup and workspace creation are not consulted.
	previous := testHandler.cfg
	testHandler.cfg.AllowSignup = false
	testHandler.cfg.DisableWorkspaceCreation = true
	testHandler.cfg.WorkspaceCreationAllowedEmails = []string{"nobody@example.com"}
	t.Cleanup(func() { testHandler.cfg = previous })

	userID, created, err := testHandler.EnsureSharedTokenUser(ctx, "  "+email+"\n")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	removeSharedTokenAccount(t, userID)
	if !created {
		t.Fatal("a database with no account for the email must have one opened")
	}

	// A finished account: found by the email, and past onboarding, which is
	// what lets a client open straight into the workspace.
	user, err := testHandler.Queries.GetUser(ctx, parseUUID(userID))
	if err != nil {
		t.Fatalf("load the account: %v", err)
	}
	if user.Email != email {
		t.Errorf("account email = %q; want %q", user.Email, email)
	}
	if !user.OnboardedAt.Valid {
		t.Error("the account must be onboarded; otherwise every client sends it back through onboarding")
	}

	// One workspace it owns, with the status catalog the create endpoint seeds.
	var slug, role string
	var statuses int64
	dbfx.QueryRow(t, `SELECT w.slug, m.role,
		(SELECT count(*) FROM issue_status s WHERE s.workspace_id = w.id)
		FROM member m JOIN workspace w ON w.id = m.workspace_id
		WHERE m.user_id = $1`, userID).Scan(&slug, &role, &statuses)
	if role != "owner" {
		t.Errorf("membership role = %q; want owner", role)
	}
	if statuses != 7 {
		t.Errorf("built-in issue statuses = %d; want the 7 the create endpoint seeds", statuses)
	}
	if !slices.Contains(sharedTokenWorkspaceSlugCandidates(email), slug) {
		t.Errorf("workspace slug = %q; want one of %v", slug, sharedTokenWorkspaceSlugCandidates(email))
	}

	// The next start finds the account and opens nothing.
	again, createdAgain, err := testHandler.EnsureSharedTokenUser(ctx, email)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if again != userID || createdAgain {
		t.Fatalf("second start = %q, created %v; want the same account %q with nothing created", again, createdAgain, userID)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM "user" WHERE email = $1`, email); n != 1 {
		t.Errorf("accounts with the email = %d; want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM member WHERE user_id = $1`, userID); n != 1 {
		t.Errorf("memberships of the account = %d; want the single first workspace", n)
	}
}

func TestEnsureSharedTokenUserUsesAnExistingAccountAsIs(t *testing.T) {
	email := newSharedTokenEmail()
	userID := dbfx.User(t, "Existing Owner", email)

	got, created, err := testHandler.EnsureSharedTokenUser(context.Background(), email)
	if err != nil {
		t.Fatalf("existing account: %v", err)
	}
	if got != userID || created {
		t.Fatalf("got %q, created %v; want the existing account %q with nothing created", got, created, userID)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM member WHERE user_id = $1`, userID); n != 0 {
		t.Errorf("memberships of the account = %d; an account that already exists must be left as it is", n)
	}
}

func TestEnsureSharedTokenUserRefusesAnAccountItMustNotOpen(t *testing.T) {
	ctx := context.Background()
	for _, email := range []string{"", "   "} {
		if _, _, err := testHandler.EnsureSharedTokenUser(ctx, email); err == nil {
			t.Errorf("email %q: expected an error, got none", email)
		}
	}
	if _, _, err := testHandler.EnsureSharedTokenUser(ctx, "pdzzer68@embassybase.com"); !errors.Is(err, auth.ErrTemporarilyDisabledUser) {
		t.Errorf("a temporarily disabled email: error = %v; want %v", err, auth.ErrTemporarilyDisabledUser)
	}
}

func TestSharedTokenWorkspaceSlugCandidates(t *testing.T) {
	first := sharedTokenWorkspaceSlugCandidates("owner@example.com")
	digest := sha256.Sum256([]byte("owner@example.com"))
	want := []string{"workspace", fmt.Sprintf("workspace-%x", digest[:3])}
	if !slices.Equal(first, want) {
		t.Fatalf("candidates = %v; want %v", first, want)
	}
	// Derived from the email: stable across starts, and not shared by accounts.
	if !slices.Equal(sharedTokenWorkspaceSlugCandidates("owner@example.com"), first) {
		t.Error("the same email must yield the same candidates on every start")
	}
	second := sharedTokenWorkspaceSlugCandidates("other@example.com")
	if second[0] != "workspace" || second[1] == first[1] {
		t.Errorf("another email = %v; want the same first candidate and a different second one", second)
	}
}
