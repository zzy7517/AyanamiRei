package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The workspace a pre-shared-token account is opened with when the database
// has none yet. The slug is what every later start derives again, so the
// account is only ever opened once.
const (
	sharedTokenFirstWorkspaceName = "Workspace"
	sharedTokenFirstWorkspaceSlug = "workspace"
)

// EnsureSharedTokenUser makes sure the account the pre-shared token belongs to
// (MULTICA_AUTH_TOKEN_EMAIL) exists and returns its ID.
//
// An account that already exists is used as it is; nothing is created for it,
// however it got there. A missing one is opened the way a finished first
// signup leaves an account: the user, a first workspace it owns, and the
// onboarded mark, in one transaction — so the next start finds the account and
// creates nothing. The mark is what lets a client open straight into the
// workspace; without it every client sends the account back through
// onboarding.
//
// The signup and workspace-creation switches do not apply. The operator named
// this account in the server's environment, which is the explicit decision
// those switches exist to gate for everyone else.
func (h *Handler) EnsureSharedTokenUser(ctx context.Context, email string) (userID string, created bool, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", false, errors.New("shared token account email is empty")
	}
	if auth.IsTemporarilyDisabledUserEmail(email) {
		return "", false, auth.ErrTemporarilyDisabledUser
	}

	userID, found, err := lookupSharedTokenUser(ctx, h.Queries, email)
	if err != nil || found {
		return userID, false, err
	}

	slug, err := h.sharedTokenWorkspaceSlug(ctx, email)
	if err != nil {
		return "", false, err
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	user, err := createUserWithEmail(ctx, qtx, email)
	if err != nil {
		if isUniqueViolation(err) {
			// Another process starting on the same fresh database got there
			// first, and opened the workspace in its own transaction.
			return lookupSharedTokenUser(ctx, h.Queries, email)
		}
		return "", false, fmt.Errorf("create user: %w", err)
	}
	if _, err := qtx.MarkUserOnboarded(ctx, user.ID); err != nil {
		return "", false, fmt.Errorf("mark user onboarded: %w", err)
	}
	if _, err := insertWorkspaceWithOwner(ctx, qtx, db.CreateWorkspaceParams{
		Name:        sharedTokenFirstWorkspaceName,
		Slug:        slug,
		IssuePrefix: defaultIssuePrefixFromSlug(slug),
	}, user.ID); err != nil {
		return "", false, fmt.Errorf("create first workspace: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	return uuidToString(user.ID), true, nil
}

// lookupSharedTokenUser finds the account an email names. found is false only
// when no account has that email.
func lookupSharedTokenUser(ctx context.Context, q *db.Queries, email string) (userID string, found bool, err error) {
	user, err := q.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
	case isNotFound(err):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("look up user: %w", err)
	}
	if auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return "", false, auth.ErrTemporarilyDisabledUser
	}
	return uuidToString(user.ID), true, nil
}

// sharedTokenWorkspaceSlug picks the slug of a pre-shared-token account's
// first workspace: the first of sharedTokenWorkspaceSlugCandidates' values
// that is not reserved and that no workspace holds.
func (h *Handler) sharedTokenWorkspaceSlug(ctx context.Context, email string) (string, error) {
	for _, slug := range sharedTokenWorkspaceSlugCandidates(email) {
		if isReservedSlug(slug) {
			continue
		}
		_, err := h.Queries.GetWorkspaceBySlug(ctx, slug)
		switch {
		case err == nil:
		case isNotFound(err):
			return slug, nil
		default:
			return "", fmt.Errorf("look up workspace slug %q: %w", slug, err)
		}
	}
	return "", fmt.Errorf("every slug for the first workspace of %s is taken", email)
}

// sharedTokenWorkspaceSlugCandidates lists the slugs a pre-shared-token
// account's first workspace may take, best first: `workspace`, then
// `workspace-` plus six hex digits of the email's digest for a deployment
// where another account already holds `workspace`.
func sharedTokenWorkspaceSlugCandidates(email string) []string {
	digest := sha256.Sum256([]byte(email))
	return []string{
		sharedTokenFirstWorkspaceSlug,
		sharedTokenFirstWorkspaceSlug + "-" + hex.EncodeToString(digest[:3]),
	}
}
