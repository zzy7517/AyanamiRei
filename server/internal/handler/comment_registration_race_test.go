package handler

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func completeAgentRun(t *testing.T, taskID string) {
	t.Helper()
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/complete", map[string]any{"output": "Processed the inputs delivered to this run"})
	req = withURLParam(req, "taskId", taskID)
	testutil.Call(t, testHandler.CompleteTask, req).Want(http.StatusOK)
}

// If completion commits while registration waits for the task row lock, the
// caller must see a miss and enqueue fresh, not attach an obligation to a run
// whose completion snapshot can no longer include it.
func TestPlannedCommentRegistrationLosesCompletionRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimeID := dbfx.Runtime(t, "Registration race")
	agentID := dbfx.Agent(t, "Registration race", runtimeID)
	issueID := dbfx.Issue(t, "Registration race")
	rootID := dbfx.Comment(t, issueID, "Initial input")
	replyID := dbfx.Comment(t, issueID, "A later reply", testutil.Cols{"parent_id": rootID})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "issue_id": issueID, "status": "running", "trigger_comment_id": rootID,
	})
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	}()
	if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}
	conn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	pid := conn.Conn().PgConn().PID()
	done := make(chan struct{})
	var registrationErr error
	go func() {
		_, registrationErr = db.New(conn).RegisterPlannedCommentForActiveTask(ctx, db.RegisterPlannedCommentForActiveTaskParams{
			IssueID: parseUUID(issueID), AgentID: parseUUID(agentID), CommentID: parseUUID(replyID),
		})
		close(done)
	}()
	// Stop the registration query before returning its connection to the pool,
	// including assertion failures while it is waiting for the task row lock.
	defer func() { cancel(); <-done }()
	for {
		var blocked bool
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("registration did not block on the completing row")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	if !errors.Is(registrationErr, pgx.ErrNoRows) {
		t.Fatalf("registration after completion = %v, want no rows", registrationErr)
	}
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(task.CoalescedCommentIds, parseUUID(replyID)) {
		t.Fatal("completed run acquired an undeliverable obligation")
	}
}
