package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDropReactionsAndShareLinksUpDownUpInIsolatedSchema runs migrations
// 740-745 in both directions in a schema that holds only the tables they
// touch, and checks what they leave behind: the tables, the indexes the
// rollback rebuilds, the seat capacity column with its checks, and the rows
// that existed only for a reaction or a share link.
func TestDropReactionsAndShareLinksUpDownUpInIsolatedSchema(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "drop_reactions_")
	pool := openTestPoolWithSearchPath(t, schema)

	exec := func(stmt string) error {
		_, err := pool.Exec(ctx, stmt)
		return err
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var out int
		if err := pool.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	indexExists := func(index string) bool {
		t.Helper()
		return count(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`, schema, index) == 1
	}
	// checkNames lists the CHECK constraints of seat_capacity_outbox, and
	// claimChecks counts those that name the share-link join.
	const checks = ` FROM pg_constraint WHERE conrelid = 'seat_capacity_outbox'::regclass AND contype = 'c'`
	checkNames := func() []string {
		names := scalar(`SELECT COALESCE(array_to_string(array_agg(conname ORDER BY conname), ','), '')` + checks)
		if names == "" {
			return nil
		}
		return strings.Split(names, ",")
	}
	claimChecks := func() int {
		return count(`SELECT count(*)` + checks + ` AND pg_get_constraintdef(oid) LIKE '%claim_share_join%'`)
	}

	// The bare shapes of the tables the migrations read or alter, and a
	// capture function that records what the reaction triggers send it.
	for _, stmt := range []string{
		`CREATE TABLE workspace (id uuid PRIMARY KEY)`,
		`CREATE TABLE issue (id uuid PRIMARY KEY, workspace_id uuid NOT NULL)`,
		`CREATE TABLE comment (id uuid PRIMARY KEY, workspace_id uuid NOT NULL, issue_id uuid NOT NULL)`,
		`CREATE TABLE inbox_item (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, workspace_id uuid NOT NULL, recipient_type text NOT NULL, recipient_id uuid NOT NULL, type text NOT NULL, title text NOT NULL)`,
		`CREATE TABLE notification_preference (workspace_id uuid NOT NULL, user_id uuid NOT NULL, preferences jsonb NOT NULL DEFAULT '{}', UNIQUE (workspace_id, user_id))`,
		`CREATE TABLE issue_wakeup (id uuid PRIMARY KEY, workspace_id uuid NOT NULL, issue_id uuid NOT NULL, agent_id uuid NOT NULL, created_by uuid NOT NULL, instruction text NOT NULL, kind text NOT NULL, mode text NOT NULL, event_types text[] NOT NULL DEFAULT '{}', condition jsonb)`,
		`CREATE TABLE issue_wakeup_receipt (id uuid PRIMARY KEY, wakeup_id uuid NOT NULL, revision bigint NOT NULL, event_key text NOT NULL, event_type text NOT NULL, payload jsonb NOT NULL)`,
		`CREATE TABLE agent_task_queue (id uuid PRIMARY KEY, agent_id uuid)`,
		`CREATE TABLE captured_wakeup (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, type text NOT NULL, payload jsonb NOT NULL)`,
		`CREATE FUNCTION capture_issue_wakeup(p_issue uuid, p_type text, p_key text, p_agent uuid, p_task uuid, p_payload jsonb) RETURNS void LANGUAGE sql AS $$
			INSERT INTO captured_wakeup (type, payload) VALUES (p_type, p_payload) $$`,
	} {
		if err := exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	run := func(direction string, versions []string) {
		t.Helper()
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
	}

	// The tables and indexes the drop removes, built as they were first shipped.
	run("up", []string{
		"026_comment_reactions", "027_issue_reactions",
		"327_workspace_share_link", "328_workspace_share_link_id_uidx", "329_workspace_share_link_primary_key",
		"330_workspace_share_link_active_ws_uidx", "331_workspace_share_link_code_uidx",
		"415_seat_capacity_outbox", "416_seat_capacity_operation_token_index", "417_seat_capacity_primary_key",
		"418_seat_capacity_due_index", "419_seat_capacity_share_join_index",
	})
	// 520 also creates triggers on tables this schema does not hold, so only
	// its reaction part is applied.
	collaboration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "520_collaboration_wakeup_events.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	from, to := bytes.Index(collaboration, []byte("CREATE FUNCTION capture_reaction_wakeup()")), bytes.Index(collaboration, []byte("CREATE FUNCTION capture_attachment_wakeup()"))
	if from < 0 || to <= from {
		t.Fatal("could not find the reaction capture in migration 520")
	}
	if err := exec(string(collaboration[from:to])); err != nil {
		t.Fatalf("create the reaction capture: %v", err)
	}
	// The indexes 740-744 drop, which the rollback rebuilds.
	concurrentIndexes := []string{"idx_seat_capacity_outbox_share_join", "workspace_share_link_code_uidx", "workspace_share_link_active_ws_uidx"}
	for _, index := range concurrentIndexes {
		assertIndexReadyAndValid(t, base, schema, index, true)
	}
	assertIndexReadyAndValid(t, base, schema, "workspace_share_link_pkey", true)
	if indexExists("workspace_share_link_pkey_uidx") {
		t.Fatal("329 left the primary key index under the name 328 built it with")
	}

	// A share link with a join intent waiting on it, next to an invitation
	// reservation; a reaction notification and its muted group; and wakeup
	// rules for reactions only, for reactions and comments, and for comments
	// only, each with receipts.
	for _, stmt := range []string{
		`INSERT INTO workspace (id) VALUES ('00000000-0000-0000-0000-00000000e001')`,
		`INSERT INTO issue (id, workspace_id) VALUES ('00000000-0000-0000-0000-00000000e002', '00000000-0000-0000-0000-00000000e001')`,
		`INSERT INTO comment (id, workspace_id, issue_id) VALUES ('00000000-0000-0000-0000-00000000e003', '00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e002')`,
		`INSERT INTO workspace_share_link (id, workspace_id, code, created_by) VALUES ('00000000-0000-0000-0000-00000000e004', '00000000-0000-0000-0000-00000000e001', 'join-code', '00000000-0000-0000-0000-00000000e005')`,
		`INSERT INTO seat_capacity_outbox (workspace_id, operation_token, action, share_link_id, user_id) VALUES ('00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e006', 'claim_share_join', '00000000-0000-0000-0000-00000000e004', '00000000-0000-0000-0000-00000000e007')`,
		`INSERT INTO seat_capacity_outbox (workspace_id, operation_token, action, invitation_id, expires_at) VALUES ('00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e008', 'reserve_invitation', '00000000-0000-0000-0000-00000000e008', now() + interval '1 day')`,
		`INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, title) VALUES ('00000000-0000-0000-0000-00000000e001', 'member', '00000000-0000-0000-0000-00000000e007', 'reaction_added', 'Reacted'), ('00000000-0000-0000-0000-00000000e001', 'member', '00000000-0000-0000-0000-00000000e007', 'new_comment', 'Commented')`,
		`INSERT INTO notification_preference (workspace_id, user_id, preferences) VALUES ('00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e007', '{"reactions":"muted","comments":"muted"}')`,
		`INSERT INTO issue_wakeup (id, workspace_id, issue_id, agent_id, created_by, instruction, kind, mode, event_types) VALUES
			('00000000-0000-0000-0000-00000000e010', '00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e002', '00000000-0000-0000-0000-00000000e009', '00000000-0000-0000-0000-00000000e005', 'reactions only', 'event', 'continuous', '{reaction.added}'),
			('00000000-0000-0000-0000-00000000e011', '00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e002', '00000000-0000-0000-0000-00000000e009', '00000000-0000-0000-0000-00000000e005', 'reactions and comments', 'event', 'continuous', '{reaction.added,comment.created}'),
			('00000000-0000-0000-0000-00000000e012', '00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e002', '00000000-0000-0000-0000-00000000e009', '00000000-0000-0000-0000-00000000e005', 'comments only', 'event', 'continuous', '{comment.created}')`,
		`INSERT INTO issue_wakeup_receipt (id, wakeup_id, revision, event_key, event_type, payload) VALUES
			('00000000-0000-0000-0000-00000000e013', '00000000-0000-0000-0000-00000000e010', 1, 'k1', 'reaction.added', '{}'),
			('00000000-0000-0000-0000-00000000e014', '00000000-0000-0000-0000-00000000e011', 1, 'k2', 'reaction.added', '{}'),
			('00000000-0000-0000-0000-00000000e015', '00000000-0000-0000-0000-00000000e011', 1, 'k3', 'comment.created', '{}'),
			('00000000-0000-0000-0000-00000000e016', '00000000-0000-0000-0000-00000000e012', 1, 'k4', 'comment.created', '{}')`,
	} {
		if err := exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	checksBefore := checkNames()

	versions := []string{
		"740_drop_seat_capacity_share_join_index",
		"741_drop_share_link_code_uidx",
		"742_drop_share_link_active_ws_uidx",
		"743_drop_share_link_primary_key",
		"744_drop_share_link_pkey_uidx",
		"745_drop_reactions_and_share_links",
	}
	for _, direction := range []string{"up", "down", "up"} {
		run(direction, versions)
		dropped := direction == "up"

		for _, table := range []string{"comment_reaction", "issue_reaction", "workspace_share_link"} {
			if exists := scalar(`SELECT (to_regclass($1) IS NOT NULL)::text`, table) == "true"; exists == dropped {
				t.Fatalf("after %s: table %s exists = %v, want %v", direction, table, exists, !dropped)
			}
		}
		// The capture function, and the two triggers that call it.
		wantCapture := 2
		if dropped {
			wantCapture = 0
		}
		if got := count(`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND p.proname = 'capture_reaction_wakeup'`, schema); got != wantCapture/2 {
			t.Fatalf("after %s: capture_reaction_wakeup functions = %d, want %d", direction, got, wantCapture/2)
		}
		if got := count(`SELECT count(*) FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND tg.tgname IN ('capture_comment_reaction_wakeup', 'capture_issue_reaction_wakeup')`, schema); got != wantCapture {
			t.Fatalf("after %s: reaction capture triggers = %d, want %d", direction, got, wantCapture)
		}
		for _, index := range concurrentIndexes {
			if indexExists(index) == dropped {
				t.Fatalf("after %s: index %s exists = %v, want %v", direction, index, dropped, !dropped)
			}
		}
		if indexExists("workspace_share_link_pkey") == dropped {
			t.Fatalf("after %s: index workspace_share_link_pkey exists = %v, want %v", direction, dropped, !dropped)
		}
		var column bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'seat_capacity_outbox' AND column_name = 'share_link_id')`, schema).Scan(&column); err != nil || column == dropped {
			t.Fatalf("after %s: seat_capacity_outbox.share_link_id exists = %v, want %v (%v)", direction, column, !dropped, err)
		}
		// Dropping share_link_id takes the check that reads it with it; the
		// rollback adds it back under the name 415 gave it.
		checksAfter := checkNames()
		if dropped {
			if claimChecks() != 0 || len(checksAfter) != len(checksBefore)-1 || !slices.Contains(checksAfter, "seat_capacity_outbox_action_check") {
				t.Fatalf("after up: checks naming claim_share_join = %d, checks = %v, before = %v", claimChecks(), checksAfter, checksBefore)
			}
		} else if claimChecks() != 2 || !slices.Equal(checksAfter, checksBefore) {
			t.Fatalf("after down: checks naming claim_share_join = %d, checks = %v, want 2 and %v", claimChecks(), checksAfter, checksBefore)
		}

		// The join intent became a release and stays one. The other rows are
		// not restored by the rollback either.
		for query, want := range map[string]string{
			`SELECT action FROM seat_capacity_outbox WHERE operation_token = '00000000-0000-0000-0000-00000000e006'`: "release",
			`SELECT action FROM seat_capacity_outbox WHERE operation_token = '00000000-0000-0000-0000-00000000e008'`: "reserve_invitation",
			`SELECT string_agg(type, ',' ORDER BY id) FROM inbox_item`:                                               "new_comment",
			`SELECT preferences::text FROM notification_preference`:                                                  `{"comments": "muted"}`,
			`SELECT string_agg(right(id::text, 3), ',' ORDER BY id) FROM issue_wakeup`:                               "011,012",
			`SELECT event_types::text FROM issue_wakeup WHERE id = '00000000-0000-0000-0000-00000000e011'`:           `{comment.created}`,
			`SELECT string_agg(event_type, ',' ORDER BY event_key) FROM issue_wakeup_receipt`:                        "comment.created,comment.created",
		} {
			if got := scalar(query); got != want {
				t.Fatalf("after %s: %s = %q, want %q", direction, query, got, want)
			}
		}
		if dropped {
			if err := exec(`INSERT INTO seat_capacity_outbox (workspace_id, operation_token, action) VALUES ('00000000-0000-0000-0000-00000000e001', '00000000-0000-0000-0000-00000000e0a1', 'claim_share_join')`); err == nil {
				t.Fatal("a share-link join intent was accepted after the drop")
			}
			continue
		}

		for _, index := range concurrentIndexes {
			assertIndexReadyAndValid(t, base, schema, index, true)
		}
		assertIndexReadyAndValid(t, base, schema, "workspace_share_link_pkey", true)
		if indexExists("workspace_share_link_pkey_uidx") {
			t.Fatal("the rollback left the primary key index under the name 328 built it with")
		}
		if got := scalar(`SELECT conname FROM pg_constraint WHERE conrelid = 'workspace_share_link'::regclass AND contype = 'p'`); got != "workspace_share_link_pkey" {
			t.Fatalf("after down: the share-link primary key constraint is %q, want the name 329 gave it", got)
		}

		// The share-link join is an intent again, and still needs its user.
		claim := `INSERT INTO seat_capacity_outbox (workspace_id, operation_token, action, share_link_id, user_id) VALUES ('00000000-0000-0000-0000-00000000e001', '%s', 'claim_share_join', '00000000-0000-0000-0000-00000000e004', %s)`
		if err := exec(fmt.Sprintf(claim, "00000000-0000-0000-0000-00000000e0a2", "'00000000-0000-0000-0000-00000000e007'")); err != nil {
			t.Fatalf("a share-link join intent was refused: %v", err)
		}
		if err := exec(fmt.Sprintf(claim, "00000000-0000-0000-0000-00000000e0a3", "NULL")); err == nil {
			t.Fatal("a share-link join intent without a user was accepted")
		}

		// A reaction is stored once per actor and emoji, and each write
		// reaches the issue's wakeup capture again.
		commentReaction := `INSERT INTO comment_reaction (comment_id, workspace_id, actor_type, actor_id, emoji) VALUES ('00000000-0000-0000-0000-00000000e003', '00000000-0000-0000-0000-00000000e001', 'member', '00000000-0000-0000-0000-00000000e007', 'eyes')`
		if err := exec(commentReaction); err != nil {
			t.Fatalf("insert a comment reaction: %v", err)
		}
		if err := exec(commentReaction); err == nil {
			t.Fatal("the same comment reaction was stored twice")
		}
		for _, stmt := range []string{
			`DELETE FROM comment_reaction WHERE comment_id = '00000000-0000-0000-0000-00000000e003'`,
			`INSERT INTO issue_reaction (issue_id, workspace_id, actor_type, actor_id, emoji) VALUES ('00000000-0000-0000-0000-00000000e002', '00000000-0000-0000-0000-00000000e001', 'member', '00000000-0000-0000-0000-00000000e007', 'eyes')`,
		} {
			if err := exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		captured := scalar(`SELECT string_agg(type || ' ' || (payload->>'target_type') || ' ' || (payload->>'emoji'), ';' ORDER BY id) FROM captured_wakeup`)
		if want := "reaction.added comment eyes;reaction.removed comment eyes;reaction.added issue eyes"; captured != want {
			t.Fatalf("captured = %q, want %q", captured, want)
		}
	}
}
