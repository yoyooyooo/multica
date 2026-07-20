package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkCoordinationMigrationRunner(t *testing.T) {
	basePool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := fmt.Sprintf("wcs_migrate_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := basePool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := basePool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	cfg := basePool.Config().Copy()
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open schema-scoped pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping schema-scoped pool: %v", err)
	}

	upFiles := workCoordinationMigrationFiles(t, "up")
	downFiles := workCoordinationMigrationFiles(t, "down")
	sort.Sort(sort.Reverse(sort.StringSlice(downFiles)))
	opts := runOptions{
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       time.Now().UnixNano(),
	}

	opts.Direction, opts.Files = "up", upFiles
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("fresh up: %v", err)
	}
	assertWorkCoordinationTables(t, ctx, pool, schema, true)
	assertWorkCoordinationSchema(t, ctx, pool, schema)

	opts.Direction, opts.Files = "down", downFiles
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertWorkCoordinationTables(t, ctx, pool, schema, false)

	opts.Direction, opts.Files = "up", upFiles
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("second up: %v", err)
	}
	assertWorkCoordinationTables(t, ctx, pool, schema, true)
	assertWorkCoordinationSchema(t, ctx, pool, schema)
}

func assertWorkCoordinationSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	type column struct {
		dataType string
		nullable string
	}
	columns := map[string]column{}
	rows, err := pool.Query(ctx, `SELECT table_name||'.'||column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name IN ('coordination_scope','coordination_receipt')`, schema)
	if err != nil {
		t.Fatalf("query coordination columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var value column
		if err := rows.Scan(&name, &value.dataType, &value.nullable); err != nil {
			t.Fatalf("scan coordination column: %v", err)
		}
		columns[name] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate coordination columns: %v", err)
	}
	expectedTypes := map[string]string{
		"coordination_scope.id": "uuid", "coordination_scope.workspace_id": "uuid", "coordination_scope.scope_kind": "text", "coordination_scope.state": "text", "coordination_scope.root_issue_id": "uuid", "coordination_scope.workflow_profile_key": "text", "coordination_scope.revision": "bigint", "coordination_scope.next_receipt_ordinal": "bigint", "coordination_scope.created_by_type": "text", "coordination_scope.created_by_id": "uuid", "coordination_scope.created_task_id": "uuid", "coordination_scope.created_at": "timestamp with time zone", "coordination_scope.updated_at": "timestamp with time zone",
		"coordination_receipt.id": "uuid", "coordination_receipt.workspace_id": "uuid", "coordination_receipt.coordination_scope_id": "uuid", "coordination_receipt.receipt_ordinal": "bigint", "coordination_receipt.operation": "text", "coordination_receipt.idempotency_key": "text", "coordination_receipt.request_hash": "bytea", "coordination_receipt.resource_type": "text", "coordination_receipt.resource_id": "uuid", "coordination_receipt.revision_before": "bigint", "coordination_receipt.revision_after": "bigint", "coordination_receipt.result_snapshot": "jsonb", "coordination_receipt.actor_type": "text", "coordination_receipt.actor_id": "uuid", "coordination_receipt.actor_task_id": "uuid", "coordination_receipt.created_at": "timestamp with time zone",
	}
	if len(columns) != len(expectedTypes) {
		t.Fatalf("coordination column count=%d want=%d: %+v", len(columns), len(expectedTypes), columns)
	}
	for name, dataType := range expectedTypes {
		got, ok := columns[name]
		if !ok || got.dataType != dataType {
			t.Fatalf("column %s=%+v want type=%s", name, got, dataType)
		}
		wantNullable := "NO"
		if name == "coordination_scope.created_task_id" || name == "coordination_receipt.actor_task_id" {
			wantNullable = "YES"
		}
		if got.nullable != wantNullable {
			t.Fatalf("column %s nullable=%s want=%s", name, got.nullable, wantNullable)
		}
	}

	var foreignKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname IN ('coordination_scope','coordination_receipt') AND c.contype='f'`, schema).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("coordination foreign keys=%d err=%v", foreignKeys, err)
	}
	constraints := map[string]string{}
	constraintRows, err := pool.Query(ctx, `SELECT c.conname,pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname IN ('coordination_scope','coordination_receipt')`, schema)
	if err != nil {
		t.Fatalf("query constraints: %v", err)
	}
	defer constraintRows.Close()
	for constraintRows.Next() {
		var name, definition string
		if err := constraintRows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan constraint: %v", err)
		}
		constraints[name] = definition
	}
	for _, name := range []string{"coordination_scope_pkey", "coordination_scope_scope_kind_check", "coordination_scope_state_check", "coordination_scope_workflow_profile_key_check", "coordination_scope_revision_check", "coordination_scope_next_receipt_ordinal_check", "coordination_scope_created_by_type_check", "coordination_scope_created_by_task_check", "coordination_receipt_pkey", "coordination_receipt_workspace_idempotency_key", "coordination_receipt_scope_ordinal_key", "coordination_receipt_receipt_ordinal_check", "coordination_receipt_operation_check", "coordination_receipt_idempotency_key_check", "coordination_receipt_request_hash_check", "coordination_receipt_resource_type_check", "coordination_receipt_revision_check", "coordination_receipt_result_snapshot_check", "coordination_receipt_actor_type_check", "coordination_receipt_actor_task_check"} {
		if _, ok := constraints[name]; !ok {
			t.Fatalf("missing coordination constraint %s: %+v", name, constraints)
		}
	}
	if !strings.Contains(constraints["coordination_receipt_request_hash_check"], "octet_length(request_hash) = 32") || !strings.Contains(constraints["coordination_receipt_result_snapshot_check"], "16384") {
		t.Fatalf("receipt hash/snapshot constraints drifted: %+v", constraints)
	}

	indexes := map[string]string{}
	indexRows, err := pool.Query(ctx, `SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename IN ('coordination_scope','coordination_receipt')`, schema)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var name, definition string
		if err := indexRows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		indexes[name] = definition
	}
	for _, name := range []string{"coordination_scope_pkey", "coordination_scope_active_natural_idx", "coordination_scope_workspace_root_idx", "coordination_receipt_pkey", "coordination_receipt_workspace_idempotency_key", "coordination_receipt_scope_ordinal_key", "coordination_receipt_scope_ordinal_desc_idx"} {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("missing coordination index %s: %+v", name, indexes)
		}
	}
	if len(indexes) != 7 || !strings.Contains(indexes["coordination_scope_active_natural_idx"], "WHERE (state = 'active'::text)") || !strings.Contains(indexes["coordination_receipt_scope_ordinal_desc_idx"], "receipt_ordinal DESC") {
		t.Fatalf("coordination indexes drifted: %+v", indexes)
	}
}

func workCoordinationMigrationFiles(t *testing.T, direction string) []string {
	t.Helper()
	dir := filepath.Clean(filepath.Join("..", "..", "migrations"))
	files := make([]string, 0, 9)
	for n := 202; n <= 210; n++ {
		matches, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%03d_coordination*.%s.sql", n, direction)))
		if err != nil || len(matches) != 1 {
			t.Fatalf("migration %03d %s: matches=%v err=%v", n, direction, matches, err)
		}
		files = append(files, matches[0])
	}
	sort.Strings(files)
	return files
}

func assertWorkCoordinationTables(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, want bool) {
	t.Helper()
	for _, table := range []string{"coordination_scope", "coordination_receipt"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if exists != want {
			t.Fatalf("table %s exists=%v, want %v", table, exists, want)
		}
	}
}
