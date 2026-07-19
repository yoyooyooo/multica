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
	seedLegacyIssueDependencies(t, ctx, pool)

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
	assertLegacyIssueDependencies(t, ctx, pool)

	opts.Direction, opts.Files = "down", downFiles
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertWorkCoordinationTables(t, ctx, pool, schema, false)
	assertLegacyIssueDependencies(t, ctx, pool)

	opts.Direction, opts.Files = "up", upFiles
	if err := runMigrations(ctx, pool, opts); err != nil {
		t.Fatalf("second up: %v", err)
	}
	assertWorkCoordinationTables(t, ctx, pool, schema, true)
	assertWorkCoordinationSchema(t, ctx, pool, schema)
	assertLegacyIssueDependencies(t, ctx, pool)
}

func seedLegacyIssueDependencies(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
CREATE TABLE issue (id UUID PRIMARY KEY);
INSERT INTO issue (id) VALUES
    ('00000000-0000-0000-0000-000000000201'), ('00000000-0000-0000-0000-000000000301'),
    ('00000000-0000-0000-0000-000000000202'), ('00000000-0000-0000-0000-000000000302'),
    ('00000000-0000-0000-0000-000000000203'), ('00000000-0000-0000-0000-000000000303');
CREATE TABLE issue_dependency (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    depends_on_issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('blocks', 'blocked_by', 'related'))
);
INSERT INTO issue_dependency (id, issue_id, depends_on_issue_id, type) VALUES
    ('00000000-0000-0000-0000-000000000101', '00000000-0000-0000-0000-000000000201', '00000000-0000-0000-0000-000000000301', 'blocks'),
    ('00000000-0000-0000-0000-000000000102', '00000000-0000-0000-0000-000000000202', '00000000-0000-0000-0000-000000000302', 'blocked_by'),
    ('00000000-0000-0000-0000-000000000103', '00000000-0000-0000-0000-000000000203', '00000000-0000-0000-0000-000000000303', 'related')`); err != nil {
		t.Fatalf("seed legacy issue dependencies: %v", err)
	}
}

func assertLegacyIssueDependencies(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT type FROM issue_dependency ORDER BY type`)
	if err != nil {
		t.Fatalf("query legacy issue dependencies: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan legacy dependency: %v", err)
		}
		got = append(got, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate legacy dependencies: %v", err)
	}
	want := []string{"blocked_by", "blocks", "related"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("legacy dependency rows=%v want=%v", got, want)
	}
	var columns string
	if err := pool.QueryRow(ctx, `SELECT string_agg(column_name,',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='issue_dependency'`).Scan(&columns); err != nil || columns != "id,issue_id,depends_on_issue_id,type" {
		t.Fatalf("legacy issue_dependency columns=%q err=%v", columns, err)
	}
	var primaryKeys, checks, cascadingForeignKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE contype='p'),count(*) FILTER (WHERE contype='c'),count(*) FILTER (WHERE contype='f' AND confdeltype='c') FROM pg_constraint WHERE conrelid='issue_dependency'::regclass`).Scan(&primaryKeys, &checks, &cascadingForeignKeys); err != nil || primaryKeys != 1 || checks != 1 || cascadingForeignKeys != 2 {
		t.Fatalf("legacy issue_dependency constraints pk=%d checks=%d cascade_fks=%d err=%v", primaryKeys, checks, cascadingForeignKeys, err)
	}
}

func assertWorkCoordinationSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) {
	t.Helper()
	type column struct {
		dataType string
		nullable string
	}
	columns := map[string]column{}
	rows, err := pool.Query(ctx, `SELECT table_name||'.'||column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name IN ('coordination_scope','coordination_receipt','coordination_dependency')`, schema)
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
		"coordination_dependency.id": "uuid", "coordination_dependency.workspace_id": "uuid", "coordination_dependency.coordination_scope_id": "uuid", "coordination_dependency.downstream_issue_id": "uuid", "coordination_dependency.upstream_issue_id": "uuid", "coordination_dependency.created_by_type": "text", "coordination_dependency.created_by_id": "uuid", "coordination_dependency.created_task_id": "uuid", "coordination_dependency.created_at": "timestamp with time zone", "coordination_dependency.resolved_by_type": "text", "coordination_dependency.resolved_by_id": "uuid", "coordination_dependency.resolved_task_id": "uuid", "coordination_dependency.resolved_at": "timestamp with time zone",
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
		if name == "coordination_scope.created_task_id" || name == "coordination_receipt.actor_task_id" || strings.HasPrefix(name, "coordination_dependency.resolved_") || name == "coordination_dependency.created_task_id" {
			wantNullable = "YES"
		}
		if got.nullable != wantNullable {
			t.Fatalf("column %s nullable=%s want=%s", name, got.nullable, wantNullable)
		}
	}

	var foreignKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname IN ('coordination_scope','coordination_receipt','coordination_dependency') AND c.contype='f'`, schema).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("coordination foreign keys=%d err=%v", foreignKeys, err)
	}
	constraints := map[string]string{}
	constraintRows, err := pool.Query(ctx, `SELECT c.conname,pg_get_constraintdef(c.oid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=$1 AND t.relname IN ('coordination_scope','coordination_receipt','coordination_dependency')`, schema)
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
	for _, name := range []string{"coordination_scope_pkey", "coordination_scope_scope_kind_check", "coordination_scope_state_check", "coordination_scope_workflow_profile_key_check", "coordination_scope_revision_check", "coordination_scope_next_receipt_ordinal_check", "coordination_scope_created_by_type_check", "coordination_scope_created_by_task_check", "coordination_receipt_pkey", "coordination_receipt_workspace_idempotency_key", "coordination_receipt_scope_ordinal_key", "coordination_receipt_receipt_ordinal_check", "coordination_receipt_operation_check", "coordination_receipt_idempotency_key_check", "coordination_receipt_request_hash_check", "coordination_receipt_resource_type_check", "coordination_receipt_revision_check", "coordination_receipt_result_snapshot_check", "coordination_receipt_actor_type_check", "coordination_receipt_actor_task_check", "coordination_dependency_pkey", "coordination_dependency_self_check", "coordination_dependency_created_by_type_check", "coordination_dependency_created_by_task_check", "coordination_dependency_resolved_by_type_check", "coordination_dependency_resolution_check"} {
		if _, ok := constraints[name]; !ok {
			t.Fatalf("missing coordination constraint %s: %+v", name, constraints)
		}
	}
	if !strings.Contains(constraints["coordination_receipt_request_hash_check"], "octet_length(request_hash) = 32") || !strings.Contains(constraints["coordination_receipt_result_snapshot_check"], "16384") {
		t.Fatalf("receipt hash/snapshot constraints drifted: %+v", constraints)
	}
	if !strings.Contains(constraints["coordination_dependency_resolution_check"], "resolved_by_type IS NOT NULL") {
		t.Fatalf("dependency resolution grouping drifted: %s", constraints["coordination_dependency_resolution_check"])
	}
	if _, err := pool.Exec(ctx, `INSERT INTO coordination_dependency (id,workspace_id,coordination_scope_id,downstream_issue_id,upstream_issue_id,created_by_type,created_by_id,resolved_by_id,resolved_at) VALUES ('00000000-0000-0000-0000-000000000501','00000000-0000-0000-0000-000000000502','00000000-0000-0000-0000-000000000503','00000000-0000-0000-0000-000000000504','00000000-0000-0000-0000-000000000505','member','00000000-0000-0000-0000-000000000506','00000000-0000-0000-0000-000000000507',now())`); err == nil {
		t.Fatal("dependency resolution without resolved_by_type was accepted")
	}

	indexes := map[string]string{}
	indexRows, err := pool.Query(ctx, `SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename IN ('coordination_scope','coordination_receipt','coordination_dependency')`, schema)
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
	for _, name := range []string{"coordination_scope_pkey", "coordination_scope_active_natural_idx", "coordination_scope_workspace_root_idx", "coordination_receipt_pkey", "coordination_receipt_workspace_idempotency_key", "coordination_receipt_scope_ordinal_key", "coordination_receipt_scope_ordinal_desc_idx", "coordination_dependency_pkey", "coordination_dependency_active_pair_idx", "coordination_dependency_scope_active_created_idx", "coordination_dependency_workspace_downstream_idx", "coordination_dependency_workspace_upstream_idx"} {
		if _, ok := indexes[name]; !ok {
			t.Fatalf("missing coordination index %s: %+v", name, indexes)
		}
	}
	if len(indexes) != 12 || !strings.Contains(indexes["coordination_scope_active_natural_idx"], "WHERE (state = 'active'::text)") || !strings.Contains(indexes["coordination_receipt_scope_ordinal_desc_idx"], "receipt_ordinal DESC") || !strings.Contains(indexes["coordination_dependency_active_pair_idx"], "WHERE (resolved_at IS NULL)") || !strings.Contains(indexes["coordination_dependency_scope_active_created_idx"], "created_at") {
		t.Fatalf("coordination indexes drifted: %+v", indexes)
	}
}

func workCoordinationMigrationFiles(t *testing.T, direction string) []string {
	t.Helper()
	dir := filepath.Clean(filepath.Join("..", "..", "migrations"))
	files := make([]string, 0, 16)
	for n := 202; n <= 217; n++ {
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
	for _, table := range []string{"coordination_scope", "coordination_receipt", "coordination_dependency"} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", schema+"."+table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if exists != want {
			t.Fatalf("table %s exists=%v, want %v", table, exists, want)
		}
	}
}
