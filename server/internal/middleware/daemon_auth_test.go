package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const daemonFixedUserID = "aaaaaaaa-0000-4000-8000-0000000000da"

type fakeDaemonAuthDB struct {
	t    *testing.T
	mode string
}

func (f *fakeDaemonAuthDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	f.t.Errorf("unexpected Exec on daemon auth path")
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (f *fakeDaemonAuthDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	f.t.Errorf("unexpected Query on daemon auth path")
	return nil, errors.New("unexpected Query")
}

func (f *fakeDaemonAuthDB) QueryRow(_ context.Context, sql string, args ...interface{}) pgx.Row {
	if strings.Contains(sql, "name: GetDaemonTokenByHash") {
		f.t.Errorf("daemon auth looked up a daemon token")
		return fakeRow{err: errors.New("daemon token lookup")}
	}
	if !strings.Contains(sql, "name: GetUserByEmail") {
		f.t.Errorf("unexpected QueryRow on daemon auth path: %s", sql)
		return fakeRow{err: errors.New("unexpected QueryRow")}
	}
	if len(args) != 1 || args[0] != SystemUserEmail {
		f.t.Errorf("GetUserByEmail args = %v, want %s", args, SystemUserEmail)
		return fakeRow{err: errors.New("unexpected email")}
	}
	switch f.mode {
	case "missing":
		return fakeRow{err: pgx.ErrNoRows}
	case "error":
		return fakeRow{err: errors.New("connection reset")}
	default:
		return fakeRow{scan: func(dest ...any) {
			*dest[0].(*pgtype.UUID) = util.MustParseUUID(daemonFixedUserID)
		}}
	}
}

func TestDaemonAuthAcceptsAnyBearerAsTheDeploymentUser(t *testing.T) {
	queries := db.New(&fakeDaemonAuthDB{t: t})
	cases := []struct {
		name   string
		header string
	}{
		{name: "no bearer"},
		{name: "bad mdt", header: "Bearer mdt_not_a_real_token"},
		{name: "task token", header: "Bearer mat_ignored_on_daemon_routes"},
		{name: "garbage", header: "Bearer not-a-credential"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotUser, gotPath, gotWorkspace, gotDaemon, gotHash, gotOwner string
			var actor, agent, task string
			handler := DaemonAuth(queries)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser = r.Header.Get("X-User-ID")
				actor = r.Header.Get("X-Actor-Source")
				agent = r.Header.Get("X-Agent-ID")
				task = r.Header.Get("X-Task-ID")
				gotPath = DaemonAuthPathFromContext(r.Context())
				gotWorkspace = DaemonWorkspaceIDFromContext(r.Context())
				gotDaemon = DaemonIDFromContext(r.Context())
				gotHash = DaemonTokenHashFromContext(r.Context())
				gotOwner = DaemonTokenOwnerIDFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/api/daemon/register", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			req.Header.Set("X-User-ID", "forged-user")
			req.Header.Set("X-Actor-Source", "agent")
			req.Header.Set("X-Agent-ID", "forged-agent")
			req.Header.Set("X-Task-ID", "forged-task")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if gotUser != daemonFixedUserID {
				t.Fatalf("X-User-ID = %q, want %s", gotUser, daemonFixedUserID)
			}
			if actor != "" || agent != "" || task != "" {
				t.Fatalf("forged identity survived: actor=%q agent=%q task=%q", actor, agent, task)
			}
			if gotPath != DaemonAuthPathDaemonToken {
				t.Fatalf("auth path = %q", gotPath)
			}
			if gotWorkspace != "" || gotDaemon != "" || gotHash != "" || gotOwner != "" {
				t.Fatalf("daemon credential context leaked: workspace=%q daemon=%q hash=%q owner=%q", gotWorkspace, gotDaemon, gotHash, gotOwner)
			}
		})
	}
}

func TestDaemonAuthReportsMissingSystemUser(t *testing.T) {
	queries := db.New(&fakeDaemonAuthDB{t: t, mode: "missing"})
	rec := httptest.NewRecorder()
	DaemonAuth(queries)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/daemon/register", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestDaemonAuthReportsSystemUserLookupFailure(t *testing.T) {
	queries := db.New(&fakeDaemonAuthDB{t: t, mode: "error"})
	rec := httptest.NewRecorder()
	DaemonAuth(queries)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/daemon/ws", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
