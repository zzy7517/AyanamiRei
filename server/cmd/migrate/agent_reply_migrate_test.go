package main

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"
)

// TestAgentRepliedAtMigrationUpDownUpInIsolatedSchema runs the
// agent_replied_at migration in both directions in a schema that holds only
// the issue table, and checks the column comes and goes with it.
func TestAgentRepliedAtMigrationUpDownUpInIsolatedSchema(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "agent_reply_")
	pool := openTestPoolWithSearchPath(t, schema)

	if _, err := pool.Exec(ctx, `CREATE TABLE issue (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, title text NOT NULL)`); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	const version = "731_issue_agent_replied_at"
	for _, direction := range []string{"up", "down", "up"} {
		if err := runMigrations(ctx, pool, runOptions{
			Direction:             direction,
			Files:                 realMigrationFiles(t, []string{version}, direction),
			SchemaMigrationsTable: schema + ".schema_migrations",
			AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
			Hooks:                 hooksForDirection(direction),
			Conditions:            conditionsForDirection(direction),
		}); err != nil {
			t.Fatalf("migrate %s: %v", direction, err)
		}

		// A fresh issue carries no marker once the column exists; without
		// the column the row cannot name it.
		var stored *time.Time
		err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title) VALUES ('00000000-0000-0000-0000-000000000001', 'agent reply') RETURNING agent_replied_at`).Scan(&stored)
		if direction == "up" {
			if err != nil || stored != nil {
				t.Fatalf("after up a fresh issue has agent_replied_at %v (%v), want none", stored, err)
			}
		} else if err == nil {
			t.Fatalf("after down an issue still has agent_replied_at")
		}
	}
}
