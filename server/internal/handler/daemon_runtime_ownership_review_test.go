package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Regression tests for the PR #9423 review (MUL-8154), derived from the
// reviewer's reproduction probes.

// registrationPrecheckBarrier delays every registration's owner pre-check
// until a second one has also read (or the window closes). Before the
// registration lock both reads saw an empty daemon and both inserted; with
// the lock the second read cannot start until the first registration is done,
// so the barrier just times out and lets each proceed in turn.
type registrationPrecheckBarrier struct {
	arrivals atomic.Int32
	release  chan struct{}
	once     sync.Once
	window   time.Duration
}

// barrierTxStarter wraps the handler's TxStarter so every transaction it
// opens routes Query through the barrier.
type barrierTxStarter struct {
	inner   txStarter
	barrier *registrationPrecheckBarrier
}

func (s barrierTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return barrierTx{Tx: tx, barrier: s.barrier}, nil
}

type barrierTx struct {
	pgx.Tx
	barrier *registrationPrecheckBarrier
}

func (t barrierTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := t.Tx.Query(ctx, query, args...)
	if err != nil {
		return rows, err
	}
	t.barrier.wait(query)
	return rows, nil
}

func (d *registrationPrecheckBarrier) wait(query string) {
	if !strings.Contains(query, "name: ListRuntimeOwnersByDaemon") {
		return
	}
	if d.arrivals.Add(1) == 2 {
		d.once.Do(func() { close(d.release) })
	}
	select {
	case <-d.release:
	case <-time.After(d.window):
	}
}

func TestDaemonRegister_ConcurrentDifferentProvidersAndProfilesHaveOneOwner(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	profileID := insertRuntimeProfileFixture(t, ctx, "Race profile "+uuid.NewString()[:8], "pi", "race-wrapper")

	cases := []struct {
		name     string
		runtimes [2]map[string]any
	}{
		{"providers", [2]map[string]any{
			{"name": "claude", "type": "claude", "status": "online"},
			{"name": "codex", "type": "codex", "status": "online"},
		}},
		{"provider and profile", [2]map[string]any{
			{"name": "claude", "type": "claude", "status": "online"},
			{"name": "race profile", "type": "pi", "profile_id": profileID, "status": "online"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			second := dbfx.User(t, "race second owner", "race-"+uuid.NewString()+"@example.com")
			dbfx.Member(t, testWorkspaceID, second, "member")
			daemonID := "race-" + uuid.NewString()
			dbfx.Exec(t, `UPDATE runtime_profile SET daemon_id = $1 WHERE id = $2`, daemonID, profileID)
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID)
			})

			// The pre-check runs on the registration transaction, so the
			// barrier is injected at the TxStarter level, not h.Queries.
			h := *testHandler
			h.TxStarter = barrierTxStarter{
				inner:   testHandler.TxStarter,
				barrier: &registrationPrecheckBarrier{release: make(chan struct{}), window: 500 * time.Millisecond},
			}
			users := []string{testUserID, second}
			codes := make([]int, 2)
			var wg sync.WaitGroup
			for i := range users {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					w := httptest.NewRecorder()
					h.DaemonRegister(w, newRequestAs(users[i], "POST", "/api/daemon/register", map[string]any{
						"workspace_id": testWorkspaceID, "daemon_id": daemonID,
						"runtimes": []map[string]any{tc.runtimes[i]},
					}))
					codes[i] = w.Code
				}(i)
			}
			wg.Wait()

			var owners, rows int
			dbfx.QueryRow(t, `SELECT count(DISTINCT owner_id), count(*) FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`,
				testWorkspaceID, daemonID).Scan(&owners, &rows)
			if owners != 1 || rows != 1 {
				t.Fatalf("daemon has %d owners across %d rows after concurrent registration (statuses %v), want exactly one", owners, rows, codes)
			}
			ok, forbidden := 0, 0
			for _, c := range codes {
				switch c {
				case http.StatusOK:
					ok++
				case http.StatusForbidden:
					forbidden++
				}
			}
			if ok != 1 || forbidden != 1 {
				t.Fatalf("statuses = %v, want one 200 and one 403", codes)
			}
		})
	}
}

func TestDaemonRegister_RefusesReservedQoderDaemonIDForUsers(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	daemonID := "qoder-managed-" + testWorkspaceID
	testutil.Call(t, testHandler.DaemonRegister, registerAs(testUserID, daemonID)).Want(http.StatusForbidden)
	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_runtime WHERE workspace_id = $1 AND daemon_id = $2`, testWorkspaceID, daemonID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d runtimes registered under the reserved Qoder daemon_id", n)
	}
}
