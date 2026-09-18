package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/forkmigrations"
)

func TestForkMigrationFreshAndInterruptedRetry(t *testing.T) {
	pool, schema, cleanup := forkMigrationTestSchema(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
CREATE TABLE workspace (id UUID PRIMARY KEY);
CREATE TABLE issue (id UUID PRIMARY KEY, workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE);
`); err != nil {
		t.Fatalf("create fresh prerequisites: %v", err)
	}
	file := forkMigrationFile(t, "001_external_pr_authority", "up")
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process exit after DDL committed but before the fork ledger row.
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply interrupted DDL: %v", err)
	}
	options := runOptions{
		Direction: "up", Files: []string{file},
		SchemaMigrationsTable: schema + ".fork_schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("retry fork migration after DDL/ledger interruption: %v", err)
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("replay applied fork migration: %v", err)
	}
	assertForkMigrationObjects(t, pool, schema)
	var ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fork_schema_migrations WHERE version='001_external_pr_authority'`).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("fork ledger rows=%d want 1", ledgerRows)
	}
}

func TestForkMigrationAcceptedFloorConvergesWithoutDroppingLegacyFinalization(t *testing.T) {
	pool, schema, cleanup := forkMigrationTestSchema(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
CREATE TABLE workspace (id UUID PRIMARY KEY);
CREATE TABLE issue (id UUID PRIMARY KEY, workspace_id UUID NOT NULL);
CREATE TABLE external_pull_request_link (
    id UUID NOT NULL DEFAULT gen_random_uuid(), workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL, provider TEXT NOT NULL, external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL, external_url TEXT, merge_provider TEXT,
    merge_repo TEXT, merge_number INTEGER, merge_url TEXT, merged_sha TEXT,
    link_confidence TEXT NOT NULL DEFAULT 'authoritative', completion_intent BOOLEAN NOT NULL DEFAULT TRUE,
    state TEXT NOT NULL DEFAULT 'open', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE external_pull_request_receipt (
    workspace_id UUID NOT NULL, idempotency_key TEXT NOT NULL, payload_hash TEXT NOT NULL,
    issue_id UUID NOT NULL, provider TEXT NOT NULL, external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE external_pr_reconcile_work (
    id UUID NOT NULL DEFAULT gen_random_uuid(), workspace_id UUID NOT NULL, issue_id UUID NOT NULL,
    link_id UUID NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL, external_repo TEXT NOT NULL,
    external_number INTEGER NOT NULL, source_revision TEXT NOT NULL, source_idempotency_key TEXT,
    state TEXT NOT NULL DEFAULT 'pending', attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 4, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner TEXT, lease_token UUID, lease_expires_at TIMESTAMPTZ, last_error_code TEXT,
    last_redacted_error TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), completed_at TIMESTAMPTZ
);
CREATE TABLE external_pr_reconcile_finalization (
    id UUID NOT NULL DEFAULT gen_random_uuid(), work_id UUID, previous_status TEXT NOT NULL,
    status_activity_id UUID NOT NULL, intended_parent_id UUID, activity_published BOOLEAN NOT NULL,
    issue_published BOOLEAN NOT NULL, parent_comment_id UUID, parent_wake_done BOOLEAN NOT NULL
);
INSERT INTO workspace VALUES ('11111111-1111-4111-8111-111111111111');
INSERT INTO issue VALUES ('22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111');
INSERT INTO external_pull_request_link (
    id,workspace_id,issue_id,provider,external_repo,external_number,external_url,
    merge_provider,merge_repo,merge_number,merge_url,merged_sha,completion_intent,state
) VALUES (
    '33333333-3333-4333-8333-333333333333','11111111-1111-4111-8111-111111111111',
    '22222222-2222-4222-8222-222222222222','ags','owner/repo',7,'https://ags.example/owner/repo/pull/7',
    'forgejo','forgejo/repo',42,'https://forgejo.example/forgejo/repo/pulls/42',
    'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',TRUE,'merged'
);
INSERT INTO external_pull_request_receipt VALUES (
    '11111111-1111-4111-8111-111111111111','accepted-floor-key','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
    '22222222-2222-4222-8222-222222222222','ags','owner/repo',7,now()
);
INSERT INTO external_pr_reconcile_work (
    id,workspace_id,issue_id,link_id,kind,provider,external_repo,external_number,source_revision,state
) VALUES (
    '44444444-4444-4444-8444-444444444444','11111111-1111-4111-8111-111111111111',
    '22222222-2222-4222-8222-222222222222','33333333-3333-4333-8333-333333333333',
    'external_pr_terminal','ags','owner/repo',7,'old-revision','retry_wait'
);
INSERT INTO external_pr_reconcile_finalization (
    work_id,previous_status,status_activity_id,intended_parent_id,activity_published,
    issue_published,parent_comment_id,parent_wake_done
) VALUES (
    '44444444-4444-4444-8444-444444444444','todo','55555555-5555-4555-8555-555555555555',
    NULL,TRUE,FALSE,NULL,FALSE
);
`); err != nil {
		t.Fatalf("create accepted-floor fixture: %v", err)
	}

	options := runOptions{
		Direction: "up", Files: []string{forkMigrationFile(t, "001_external_pr_authority", "up")},
		SchemaMigrationsTable: schema + ".fork_schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("converge accepted floor: %v", err)
	}
	assertForkMigrationObjects(t, pool, schema)
	var revision, receiptLink, previousStatus string
	var activityPublished, issuePublished bool
	if err := pool.QueryRow(ctx, `SELECT fact_revision FROM external_pull_request_link`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" {
		t.Fatalf("receipt-backed fact revision=%q", revision)
	}
	if err := pool.QueryRow(ctx, `SELECT link_id::text FROM external_pull_request_receipt`).Scan(&receiptLink); err != nil {
		t.Fatal(err)
	}
	if receiptLink != "33333333-3333-4333-8333-333333333333" {
		t.Fatalf("receipt link=%s", receiptLink)
	}
	if err := pool.QueryRow(ctx, `SELECT previous_status,activity_published,issue_published FROM external_pr_reconcile_work`).Scan(&previousStatus, &activityPublished, &issuePublished); err != nil {
		t.Fatal(err)
	}
	if previousStatus != "todo" || !activityPublished || issuePublished {
		t.Fatalf("folded finalization=%s/%v/%v", previousStatus, activityPublished, issuePublished)
	}
	var legacyStillPresent bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('external_pr_reconcile_finalization') IS NOT NULL`).Scan(&legacyStillPresent); err != nil {
		t.Fatal(err)
	}
	if !legacyStillPresent {
		t.Fatal("accepted-floor finalization evidence was dropped")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM issue WHERE id='22222222-2222-4222-8222-222222222222'`); err != nil {
		t.Fatalf("delete Issue through atomic cleanup trigger: %v", err)
	}
	for _, table := range []string{"external_pull_request_link", "external_pull_request_receipt", "external_pr_reconcile_work"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after Issue delete=%d", table, count)
		}
	}
}

func forkMigrationTestSchema(t *testing.T) (*pgxpool.Pool, string, func()) {
	t.Helper()
	admin := openTestPool(t)
	schema := fmt.Sprintf("fork_migration_%d_%d", time.Now().UnixNano(), rand.Uint32())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	pool := openTestPoolWithSearchPath(t, schema)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+identifier+" CASCADE"); err != nil {
			t.Logf("drop test schema: %v", err)
		}
	}
	return pool, schema, cleanup
}

func forkMigrationFile(t *testing.T, version, direction string) string {
	t.Helper()
	files, err := forkmigrations.Files(direction)
	if err != nil {
		t.Fatal(err)
	}
	want := version + "." + direction + ".sql"
	for _, file := range files {
		if filepath.Base(file) == want {
			return file
		}
	}
	t.Fatalf("fork migration %s not found", want)
	return ""
}

func assertForkMigrationObjects(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema string) {
	t.Helper()
	for _, table := range []string{"external_pull_request_link", "external_pull_request_receipt", "external_pr_reconcile_work", "fork_schema_migrations"} {
		var exists bool
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, schema+"."+table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("%s missing", table)
		}
	}
}
