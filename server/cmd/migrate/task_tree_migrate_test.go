package main

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// TestTaskTreeMigrationsUpDownUpInIsolatedSchema runs the daemon_id and
// completed_at migrations in both directions in a schema that holds only the
// tables they touch, and checks what they leave behind: the column, the
// trigger, and when the trigger stamps a task.
func TestTaskTreeMigrationsUpDownUpInIsolatedSchema(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "task_tree_")
	pool := openTestPoolWithSearchPath(t, schema)

	// The bare shapes of the tables the two migrations alter, and the custom
	// status the trigger reads the category of.
	for _, stmt := range []string{
		`CREATE TABLE issue_project_scope (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, issue_id uuid NOT NULL, project_id uuid NOT NULL, base_branch text NOT NULL)`,
		`CREATE TABLE issue_status (workspace_id uuid NOT NULL, key text NOT NULL, category text NOT NULL)`,
		`CREATE TABLE issue (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, title text NOT NULL, status text NOT NULL DEFAULT 'todo')`,
		`INSERT INTO issue_status (workspace_id, key, category) VALUES ('00000000-0000-0000-0000-000000000001', 'shipped', 'done')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	versions := []string{"729_issue_project_scope_daemon_id", "730_issue_completed_at"}
	for _, direction := range []string{"up", "down", "up"} {
		files := slices.Clone(versions)
		if direction == "down" {
			slices.Reverse(files)
		}
		if err := runMigrations(ctx, pool, runOptions{
			Direction:             direction,
			Files:                 realMigrationFiles(t, files, direction),
			SchemaMigrationsTable: schema + ".schema_migrations",
			AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
			Hooks:                 hooksForDirection(direction),
			Conditions:            conditionsForDirection(direction),
		}); err != nil {
			t.Fatalf("migrate %s: %v", direction, err)
		}

		want := direction == "up"
		var daemonID, completedAt, trigger bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'issue_project_scope' AND column_name = 'daemon_id'),
			       EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'issue' AND column_name = 'completed_at'),
			       EXISTS (SELECT 1 FROM information_schema.triggers WHERE trigger_schema = $1 AND trigger_name = 'trg_issue_completed_at')`, schema).
			Scan(&daemonID, &completedAt, &trigger)
		if err != nil || daemonID != want || completedAt != want || trigger != want {
			t.Fatalf("after %s: daemon_id=%v completed_at=%v trigger=%v, want %v of each (%v)", direction, daemonID, completedAt, trigger, want, err)
		}
		if !want {
			continue
		}

		// daemon_id is empty until a daemon reports, and keeps what it stored.
		var treeID string
		var stored *string
		if err := pool.QueryRow(ctx, `INSERT INTO issue_project_scope (workspace_id, issue_id, project_id, base_branch)
			VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000003', 'main') RETURNING id`).Scan(&treeID); err != nil {
			t.Fatalf("insert a tree: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT daemon_id FROM issue_project_scope WHERE id = $1::uuid`, treeID).Scan(&stored); err != nil || stored != nil {
			t.Fatalf("a tree nobody reported about is hosted by %v (%v), want none", stored, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE issue_project_scope SET daemon_id = 'daemon-1' WHERE id = $1::uuid`, treeID); err != nil {
			t.Fatalf("store the reporting daemon: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT daemon_id FROM issue_project_scope WHERE id = $1::uuid`, treeID).Scan(&stored); err != nil || stored == nil || *stored != "daemon-1" {
			t.Fatalf("a reported tree is hosted by %v (%v), want daemon-1", stored, err)
		}

		// The trigger stamps a task by the category of its status, whichever
		// path writes it, and clears it for any other category.
		var issueID string
		if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title) VALUES ('00000000-0000-0000-0000-000000000001', 'Task') RETURNING id`).Scan(&issueID); err != nil {
			t.Fatalf("insert a task: %v", err)
		}
		stamp := func() *string {
			t.Helper()
			var at *string
			if err := pool.QueryRow(ctx, `SELECT completed_at::text FROM issue WHERE id = $1::uuid`, issueID).Scan(&at); err != nil {
				t.Fatalf("read completed_at: %v", err)
			}
			return at
		}
		expect := func(step string, set bool) {
			t.Helper()
			if at := stamp(); (at != nil) != set {
				t.Fatalf("after %s: completed_at = %v, want set = %v", step, at, set)
			}
		}
		write := func(stmt string) {
			t.Helper()
			if _, err := pool.Exec(ctx, stmt, issueID); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		expect("insert", false)
		write(`UPDATE issue SET status = 'done' WHERE id = $1::uuid`)
		expect("done", true)
		write(`UPDATE issue SET title = 'Renamed' WHERE id = $1::uuid`)
		expect("renaming a closed task", true)
		write(`UPDATE issue SET status = 'backlog' WHERE id = $1::uuid`)
		expect("backlog", false)
		write(`UPDATE issue SET status = 'shipped' WHERE id = $1::uuid`)
		expect("a custom done status", true)
		write(`UPDATE issue SET status = 'todo' WHERE id = $1::uuid`)
		expect("todo", false)
	}
}
