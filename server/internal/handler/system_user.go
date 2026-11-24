package handler

import (
	"context"
	"fmt"

	"encoding/json"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type UserResponse struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	Email                   string          `json:"email"`
	AvatarURL               *string         `json:"avatar_url"`
	Language                *string         `json:"language"`
	Timezone                *string         `json:"timezone"`
	OnboardedAt             *string         `json:"onboarded_at"`
	OnboardingQuestionnaire json.RawMessage `json:"onboarding_questionnaire"`
	ProfileDescription      string          `json:"profile_description"`
	WhatsNewSeenVersion     *string         `json:"whats_new_seen_version"`
	StarPromptShownCount    int32           `json:"star_prompt_shown_count"`
	StarPromptLastShownAt   *string         `json:"star_prompt_last_shown_at"`
	StarPromptStarredAt     *string         `json:"star_prompt_starred_at"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
}

const (
	SystemUserEmail     = "dev@localhost"
	systemWorkspaceName = "Dev"
	systemWorkspaceSlug = "dev"
)

// EnsureSystemUser makes the single deployment identity and its default
// workspace available on a fresh database. Existing rows are used unchanged;
// the transaction makes a concurrent cold start create either none or all of
// the bootstrap rows.
func (h *Handler) EnsureSystemUser(ctx context.Context) (userID string, created bool, err error) {
	userID, found, err := lookupSystemUser(ctx, h.Queries)
	if err != nil || found {
		return userID, false, err
	}

	_, err = h.Queries.GetWorkspaceBySlug(ctx, systemWorkspaceSlug)
	switch {
	case err == nil:
		return "", false, fmt.Errorf("workspace %q exists but user %q is missing", systemWorkspaceSlug, SystemUserEmail)
	case !isNotFound(err):
		return "", false, fmt.Errorf("look up workspace %q: %w", systemWorkspaceSlug, err)
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return "", false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	user, err := qtx.CreateUser(ctx, db.CreateUserParams{
		Name:  "dev",
		Email: SystemUserEmail,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return lookupSystemUser(ctx, h.Queries)
		}
		return "", false, fmt.Errorf("create system user: %w", err)
	}
	if _, err := qtx.MarkUserOnboarded(ctx, user.ID); err != nil {
		return "", false, fmt.Errorf("mark system user onboarded: %w", err)
	}
	if _, err := insertWorkspaceWithOwner(ctx, qtx, db.CreateWorkspaceParams{
		Name:        systemWorkspaceName,
		Slug:        systemWorkspaceSlug,
		IssuePrefix: defaultIssuePrefixFromSlug(systemWorkspaceSlug),
	}, user.ID); err != nil {
		return "", false, fmt.Errorf("create default workspace: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, fmt.Errorf("commit: %w", err)
	}
	return uuidToString(user.ID), true, nil
}

func lookupSystemUser(ctx context.Context, q *db.Queries) (string, bool, error) {
	user, err := q.GetUserByEmail(ctx, SystemUserEmail)
	switch {
	case err == nil:
		return uuidToString(user.ID), true, nil
	case isNotFound(err):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("look up system user: %w", err)
	}
}
