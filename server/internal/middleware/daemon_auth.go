package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Daemon context keys.
type daemonContextKey int

const (
	ctxKeyDaemonWorkspaceID daemonContextKey = iota
	ctxKeyDaemonID
	ctxKeyDaemonAuthPath
	ctxKeyDaemonTokenHash
	ctxKeyDaemonTokenOwnerID
)

const DaemonAuthPathDaemonToken = "daemon_token"

// DaemonWorkspaceIDFromContext returns a workspace bound by an in-process
// daemon context. HTTP daemon routes do not set it.
func DaemonWorkspaceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonWorkspaceID).(string)
	return id
}

// DaemonIDFromContext returns a daemon id bound by an in-process daemon
// context. HTTP daemon routes do not set it.
func DaemonIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonID).(string)
	return id
}

// DaemonAuthPathFromContext reports whether a request passed through the
// daemon route middleware.
func DaemonAuthPathFromContext(ctx context.Context) string {
	p, _ := ctx.Value(ctxKeyDaemonAuthPath).(string)
	return p
}

// DaemonTokenHashFromContext returns a daemon-token hash attached for
// in-process replay. HTTP daemon routes do not set it.
func DaemonTokenHashFromContext(ctx context.Context) string {
	h, _ := ctx.Value(ctxKeyDaemonTokenHash).(string)
	return h
}

// DaemonTokenOwnerIDFromContext returns a runtime owner attached for
// in-process replay. HTTP daemon routes do not set it.
func DaemonTokenOwnerIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyDaemonTokenOwnerID).(string)
	return id
}

// WithDaemonTokenOwnerID attaches a runtime owner for in-process replay.
func WithDaemonTokenOwnerID(ctx context.Context, ownerID string) context.Context {
	if ownerID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyDaemonTokenOwnerID, ownerID)
}

// WithDaemonTokenHash attaches a daemon-token hash for in-process replay.
func WithDaemonTokenHash(ctx context.Context, tokenHash string) context.Context {
	if tokenHash == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyDaemonTokenHash, tokenHash)
}

func WithDaemonContext(ctx context.Context, workspaceID, daemonID string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyDaemonWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxKeyDaemonID, daemonID)
	ctx = context.WithValue(ctx, ctxKeyDaemonAuthPath, DaemonAuthPathDaemonToken)
	return ctx
}

// DaemonAuth admits every daemon connection as the deployment user. Daemon
// routes do not check a bearer token. Registration still binds the runtime
// to that user through the existing membership path. Task-scoped mat_ tokens
// on the human API are unchanged.
func DaemonAuth(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Del("X-Actor-Source")
			r.Header.Del("X-Agent-ID")
			r.Header.Del("X-Task-ID")
			r.Header.Del("X-User-ID")

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
			ctx := context.WithValue(r.Context(), ctxKeyDaemonAuthPath, DaemonAuthPathDaemonToken)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
