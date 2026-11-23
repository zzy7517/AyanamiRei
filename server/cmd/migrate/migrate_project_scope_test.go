package main

import (
	"context"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// TestProjectScopeMigrationsUpDownUpInIsolatedSchema runs the task project
// scope migrations in both directions and checks what they leave behind: the
// table, its indexes, one row per pair, and the tree states a row accepts.
func TestProjectScopeMigrationsUpDownUpInIsolatedSchema(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "project_scope_")
	pool := openTestPoolWithSearchPath(t, schema)

	versions := []string{
		"726_issue_project_scope",
		"727_issue_project_scope_pair_idx",
		"728_issue_project_scope_project_idx",
	}
	indexes := []string{
		"issue_project_scope_pkey",
		"uq_issue_project_scope_pair",
		"idx_issue_project_scope_project",
	}
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
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+".issue_project_scope").Scan(&exists); err != nil || exists != (direction == "up") {
			t.Fatalf("after %s, table issue_project_scope exists=%v: %v", direction, exists, err)
		}
		if direction != "up" {
			continue
		}
		var valid int
		err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND i.indisvalid AND c.relname = ANY($2::text[])
		`, schema, indexes).Scan(&valid)
		if err != nil || valid != len(indexes) {
			t.Fatalf("valid project scope indexes = %d, want %d: %v", valid, len(indexes), err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// A project may appear once per base branch, for each task.
		_, err = tx.Exec(ctx, `
			INSERT INTO issue_project_scope (workspace_id, issue_id, project_id, base_branch) VALUES
				('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c001', '00000000-0000-0000-0000-00000000b001', 'main'),
				('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c001', '00000000-0000-0000-0000-00000000b001', 'develop'),
				('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c002', '00000000-0000-0000-0000-00000000b001', 'main');
		`)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("distinct pairs were refused: %v", err)
		}
		var state string
		err = tx.QueryRow(ctx, `SELECT state FROM issue_project_scope WHERE issue_id = '00000000-0000-0000-0000-00000000c001' AND base_branch = 'main'`).Scan(&state)
		_ = tx.Rollback(ctx)
		if err != nil || state != "pending" {
			t.Fatalf("a new task pair starts in state %q, want pending: %v", state, err)
		}

		sameTaskPair := `INSERT INTO issue_project_scope (workspace_id, issue_id, project_id, base_branch)
			VALUES ('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c001', '00000000-0000-0000-0000-00000000b001', 'main')`

		// Each refused statement runs after the row it needs or collides with,
		// in one transaction, so only the second one can be what fails.
		refused := []struct{ name, first, second string }{
			{
				name:   "the same task, project and base branch twice",
				first:  sameTaskPair,
				second: sameTaskPair,
			},
			{
				name:  "a tree state the table does not know",
				first: sameTaskPair,
				second: `INSERT INTO issue_project_scope (workspace_id, issue_id, project_id, base_branch, state)
					VALUES ('00000000-0000-0000-0000-00000000d001', '00000000-0000-0000-0000-00000000c009', '00000000-0000-0000-0000-00000000b001', 'main', 'creating')`,
			},
		}
		for _, tc := range refused {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, tc.first)
			if err == nil {
				_, err = tx.Exec(ctx, tc.second)
			}
			_ = tx.Rollback(ctx)
			if err == nil {
				t.Fatalf("the database accepted %s", tc.name)
			}
		}
	}
}
