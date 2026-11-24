package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const SystemUserEmail = "dev@localhost"

// FixedUser resolves every request to the one local deployment identity. The
// router ensures the user exists during startup; resolution still checks the
// row so a database reset produces an actionable failure rather than a request
// acting as a phantom user.
func FixedUser(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Del("X-Actor-Source")
			r.Header.Del("X-Agent-ID")
			r.Header.Del("X-Task-ID")
			r.Header.Del("X-User-ID")

			if tokenString, ok := bearerToken(r); ok && strings.HasPrefix(tokenString, "mat_") {
				token, err := queries.GetTaskTokenByHash(r.Context(), auth.HashToken(tokenString))
				if err != nil {
					slog.Warn("invalid task token", "path", r.URL.Path, "error", err)
					writeError(w, http.StatusUnauthorized, "invalid token")
					return
				}
				boundWorkspaceID := util.UUIDToString(token.WorkspaceID)
				if checkTaskTokenWorkspaceTarget(w, r, queries, boundWorkspaceID) {
					return
				}
				r.Header.Set("X-User-ID", util.UUIDToString(token.UserID))
				r.Header.Set("X-Agent-ID", util.UUIDToString(token.AgentID))
				r.Header.Set("X-Task-ID", util.UUIDToString(token.TaskID))
				r.Header.Set("X-Workspace-ID", boundWorkspaceID)
				r.Header.Set("X-Actor-Source", "task_token")
				next.ServeHTTP(w, r)
				return
			}

			userID, err := ResolveFixedUserID(r.Context(), queries)
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Error("fixed system user is missing", "email", SystemUserEmail)
				writeError(w, http.StatusServiceUnavailable, "system user unavailable")
				return
			}
			if err != nil {
				slog.Error("resolve fixed system user", "error", err)
				writeError(w, http.StatusInternalServerError, "system user unavailable")
				return
			}

			r.Header.Set("X-User-ID", userID)
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

func ResolveFixedUserID(ctx context.Context, queries *db.Queries) (string, error) {
	user, err := queries.GetUserByEmail(ctx, SystemUserEmail)
	if err != nil {
		return "", err
	}
	return util.UUIDToString(user.ID), nil
}
