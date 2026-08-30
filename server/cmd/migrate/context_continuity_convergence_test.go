//go:build contextcontinuitydb

package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const contextContinuityTestDB = "multica_cc_upstream_main_20260808"

func TestContextContinuityMigrationConvergence(t *testing.T) {
	dbURL := requireContextContinuityTestDatabase(t)
	ensureContextContinuityDatabaseExtensions(t, dbURL)
	for _, scenario := range []string{"clean_v0_4_12", "historical_135", "fork_v0_4_8", "fork_v0_4_9"} {
		t.Run(scenario, func(t *testing.T) {
			testContextContinuityMigrationScenario(t, dbURL, scenario)
		})
	}
}

func requireContextContinuityTestDatabase(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Fatal("DATABASE_URL is required for the context-continuity migration matrix")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("DATABASE_URL scheme = %q, want postgres", parsed.Scheme)
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Port() != "55449" || strings.TrimPrefix(parsed.Path, "/") != contextContinuityTestDB {
		t.Fatalf("unsafe migration test target host=%q port=%q database=%q", parsed.Hostname(), parsed.Port(), strings.TrimPrefix(parsed.Path, "/"))
	}
	return raw
}

func ensureContextContinuityDatabaseExtensions(t *testing.T, dbURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public`); err != nil {
		t.Fatalf("install pg_trgm in public: %v", err)
	}
}

func testContextContinuityMigrationScenario(t *testing.T, dbURL, scenario string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("open dedicated admin pool: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Fatalf("ping dedicated database: %v", err)
	}

	schema := fmt.Sprintf("cc_upstream_main_%s_%d_%d", scenario, time.Now().UnixNano(), rand.Uint32())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create audit schema: %v", err)
	}
	t.Logf("audit schema retained: %s", schema)

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatalf("parse dedicated database config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open scenario pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping scenario pool: %v", err)
	}

	serverRoot, err := filepath.Abs(filepath.Clean(filepath.Join("..", "..")))
	if err != nil {
		t.Fatal(err)
	}
	tableName := schema + ".schema_migrations"
	tableFQN := pgx.Identifier{schema, "schema_migrations"}.Sanitize()
	lockKey := int64(rand.Uint64()&0x7fffffffffffffff) | 1
	var appliedFiles []string
	contextContinuityHooks := reconciliationMigrationHooks
	run := func(files []string) {
		t.Helper()
		if err := runMigrations(ctx, pool, runOptions{
			Direction:               "up",
			Files:                   files,
			SchemaMigrationsTable:   tableName,
			AdvisoryLockKey:         lockKey,
			Hooks:                   preMigrationHooks,
			ReconcileHooks:          contextContinuityHooks,
			PostFenceReconcileHooks: postFenceReconciliationHooks,
			ReconcileFenceVersion:   externalPRIndexReconciliationFenceVersion,
		}); err != nil {
			t.Fatalf("run migrations: %v", err)
		}
		appliedFiles = append(appliedFiles, files...)
	}

	var wantLegacyRow bool
	switch scenario {
	case "clean_v0_4_12":
		run(realMigrationRange(t, serverRoot, 1, 264))
		seedCurrentBaseRows(t, ctx, pool)
	case "historical_135":
		run(realMigrationRange(t, serverRoot, 1, 134))
		run([]string{filepath.Join(serverRoot, "cmd", "migrate", "testdata", "context-continuity", "historical135", "135_external_pr_integration.up.sql")})
		seedCurrentBaseRows(t, ctx, pool)
		seedLegacyExternalPRRow(t, ctx, pool)
		run(realMigrationRange(t, serverRoot, 135, 264))
		wantLegacyRow = true
	case "fork_v0_4_8":
		run(realMigrationRange(t, serverRoot, 1, 211))
		run(globMigrationFiles(t, filepath.Join(serverRoot, "cmd", "migrate", "testdata", "context-continuity", "fork-v0.4.8", "*.up.sql")))
		seedCurrentBaseRows(t, ctx, pool)
		seedLegacyExternalPRRow(t, ctx, pool)
		run(realMigrationRange(t, serverRoot, 212, 264))
		wantLegacyRow = true
	case "fork_v0_4_9":
		run(realMigrationRange(t, serverRoot, 1, 223))
		seedCurrentBaseRows(t, ctx, pool)
		run(globMigrationFiles(t, filepath.Join(serverRoot, "cmd", "migrate", "testdata", "context-continuity", "fork-v0.4.9", "*.up.sql")))
		// The deployed v0.4.9 ledger already owns its historical fork migration
		// names. Apply the frozen upstream/main migrations through 264 afterward, then
		// prove the newly numbered 265-277 reconciliation is idempotent.
		run(realMigrationRange(t, serverRoot, 224, 264))
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}

	// Formal generation numbers (fork/v0.4.22+):
	// 265-271 upstream issue_view band; 272-276 External PR core indexes;
	// 277-279 workload authority; 280-284 External PR cleanup/fence;
	// 285-291 delegated merge; 292-299 T016 retirement of dead authority.
	run(realMigrationRange(t, serverRoot, 265, 276))

	// Authority table + temporary member trigger live after 277.
	run(realMigrationRange(t, serverRoot, 277, 277))
	var epochBefore int64
	if err := pool.QueryRow(ctx, `SELECT membership_epoch FROM workspace_workload_authority
WHERE workspace_id='00000000-0000-4000-8000-000000000049'`).Scan(&epochBefore); err != nil {
		t.Fatalf("read epoch before inter-migration member change: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE member SET role='admin' WHERE id='00000000-0000-4000-8000-000000000249'`); err != nil {
		t.Fatalf("mutate member between 277 and 278: %v", err)
	}
	var epochAfterTemporaryTrigger int64
	if err := pool.QueryRow(ctx, `SELECT membership_epoch FROM workspace_workload_authority
WHERE workspace_id='00000000-0000-4000-8000-000000000049'`).Scan(&epochAfterTemporaryTrigger); err != nil {
		t.Fatalf("read epoch after temporary trigger: %v", err)
	}
	if epochAfterTemporaryTrigger != epochBefore+1 {
		t.Fatalf("temporary member trigger epoch=%d, want %d", epochAfterTemporaryTrigger, epochBefore+1)
	}
	run(realMigrationRange(t, serverRoot, 278, 279))

	// External PR workspace-scoped idempotency + reconciliation fence path.
	migration280 := realMigrationRange(t, serverRoot, 280, 280)
	exerciseMigration273Recovery(t, ctx, pool, tableFQN, tableName, lockKey, migration280, scenario)
	run(migration280)
	assertWorkspaceIdempotencyIntermediate(t, ctx, pool, schema, scenario)
	exerciseLedgeredIndexRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	exerciseMigration274Gate(t, ctx, pool, serverRoot, tableName, lockKey)
	run(realMigrationRange(t, serverRoot, 281, 281))
	prepareReconciliationFenceRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 282, 282))
	assertReconciliationFenceStopsHistoricalRepair(t, ctx, pool, serverRoot, tableName, lockKey)
	exerciseCleanupIndexRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 283, 284))
	exerciseCleanupIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	for _, spec := range externalPRCleanupIndexSpecs {
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("cleanup index %s not exact after recovery: exact=%v err=%v", spec.Name, exact, err)
		}
	}

	// Delegated merge schema + concurrent indexes (formal 285-291).
	run(realMigrationRange(t, serverRoot, 285, 285))
	seedPRMergeIndexRecoveryRows(t, ctx, pool)
	exercisePRMergeIndexWrongDefinitionRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 286, 291))
	exercisePRMergeIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	for _, spec := range prMergeDelegationIndexSpecs {
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("delegated merge index %s not exact after recovery: exact=%v err=%v", spec.Name, exact, err)
		}
	}

	// T016: retire dead authority/delegation schema after product indexes are proven.
	run(realMigrationRange(t, serverRoot, 292, 299))
	// T017: drop link-row projection/idempotency columns; receipt remains owner.
	run(realMigrationRange(t, serverRoot, 300, 301))

	// The typed continuation seam is forward-only and independent from the
	// historical delegation audit range. Exercise clean creation, wrong
	// definitions, invalid concurrent artifacts, and a second-run no-op.
	run(realMigrationRange(t, serverRoot, 302, 302))
	exerciseExternalPRReconcileIndexWrongDefinitionRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 303, 305))
	exerciseExternalPRReconcileIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	for _, spec := range externalPRReconcileIndexSpecs {
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("external PR reconcile index %s not exact after recovery: exact=%v err=%v", spec.Name, exact, err)
		}
	}
	run(realMigrationRange(t, serverRoot, 306, 306))
	exerciseExternalPRFinalizationHistoricalStateConstraint(t, ctx, pool)
	exerciseExternalPRFinalizationIndexWrongDefinitionRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 307, 311))
	exerciseExternalPRInboxIndexWrongDefinitionRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 312, 312))
	exerciseExternalPRInboxIndexLedgeredWrongDefinitionRecovery(t, ctx, pool, serverRoot, tableName, lockKey)
	exerciseExternalPRFinalizationRetryStateRollback(t, ctx, pool, serverRoot, tableName, lockKey)
	assertExternalPRFinalizationLeaseColumns(t, ctx, pool)
	assertExternalPRFinalizationStateConstraint(t, ctx, pool)
	exerciseExternalPRFinalizationIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	exerciseExternalPRInboxIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)

	// 313-314 build the new-table primary-key indexes independently; 315-316
	// attach those indexes as constraints without creating another index.
	exerciseExternalPRPrimaryKeyIndexWrongDefinitionRecovery(t, ctx, pool)
	run(realMigrationRange(t, serverRoot, 313, 314))
	exerciseExternalPRPrimaryKeyIndexInvalidRecovery(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	run(realMigrationRange(t, serverRoot, 315, 316))
	assertExternalPRPrimaryKeyConstraints(t, ctx, pool)
	exerciseExternalPRPrimaryKeyLedgerGap(t, ctx, pool, serverRoot, tableFQN, tableName, lockKey)
	exerciseExternalPRPrimaryKeyWrongAuthorityRecovery(t, ctx, pool, serverRoot, tableName, lockKey)
	exerciseExternalPRPrimaryKeyDownUp(t, ctx, pool, serverRoot, tableName, lockKey)
	assertExternalPRPrimaryKeyConstraints(t, ctx, pool)
	for _, spec := range externalPRFinalizationIndexSpecs {
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("external PR finalization index %s not exact after recovery: exact=%v err=%v", spec.Name, exact, err)
		}
	}
	for _, spec := range externalPRInboxIndexSpecs {
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("external PR inbox index %s not exact after recovery: exact=%v err=%v", spec.Name, exact, err)
		}
	}

	assertContextContinuitySchema(t, ctx, pool, schema, scenario, wantLegacyRow)
	assertWorkspaceScopedIdempotency(t, ctx, pool)
	ledgerBefore := migrationLedgerSnapshot(t, ctx, pool, tableFQN)
	catalogBefore := contextContinuityCatalogSnapshot(t, ctx, pool, schema)
	legacyBefore := legacyExternalPRSnapshot(t, ctx, pool)

	// A second real-runner pass over every exact file must make zero changes to
	// the complete ledger, relevant catalog definitions, triggers, and legacy
	// facts. No schema_migrations row is inserted or edited by the fixture.
	if err := runMigrations(ctx, pool, runOptions{
		Direction:               "up",
		Files:                   appliedFiles,
		SchemaMigrationsTable:   tableName,
		AdvisoryLockKey:         lockKey,
		Hooks:                   preMigrationHooks,
		ReconcileHooks:          contextContinuityHooks,
		PostFenceReconcileHooks: postFenceReconciliationHooks,
		ReconcileFenceVersion:   externalPRIndexReconciliationFenceVersion,
	}); err != nil {
		t.Fatalf("second migration runner pass: %v", err)
	}
	ledgerAfter := migrationLedgerSnapshot(t, ctx, pool, tableFQN)
	catalogAfter := contextContinuityCatalogSnapshot(t, ctx, pool, schema)
	legacyAfter := legacyExternalPRSnapshot(t, ctx, pool)
	if !reflect.DeepEqual(ledgerAfter, ledgerBefore) {
		t.Fatalf("complete ledger changed on rerun\nbefore=%v\nafter=%v", ledgerBefore, ledgerAfter)
	}
	if !reflect.DeepEqual(catalogAfter, catalogBefore) {
		t.Fatalf("catalog changed on rerun\nbefore=%v\nafter=%v", catalogBefore, catalogAfter)
	}
	if legacyAfter != legacyBefore {
		t.Fatalf("legacy facts changed on rerun: before=%q after=%q", legacyBefore, legacyAfter)
	}
	assertContextContinuitySchema(t, ctx, pool, schema, scenario, wantLegacyRow)
}

func assertExternalPRFinalizationStateConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	var definitions string
	if err := pool.QueryRow(ctx, `
SELECT count(*)::int, COALESCE(string_agg(pg_get_constraintdef(oid), ';' ORDER BY conname), '')
FROM pg_constraint
WHERE conrelid='external_pr_reconcile_finalization'::regclass
  AND contype='c'
  AND conname LIKE 'external_pr_reconcile_finalization%state%check'`).Scan(&count, &definitions); err != nil {
		t.Fatalf("inspect finalization state constraint: %v", err)
	}
	if count != 1 || !strings.Contains(strings.ToLower(definitions), "retry_wait") {
		t.Fatalf("finalization state constraints=%d definitions=%q, want one constraint allowing retry_wait", count, definitions)
	}
}

func assertExternalPRFinalizationLeaseColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::int
FROM information_schema.columns
WHERE table_schema=current_schema()
  AND table_name='external_pr_reconcile_finalization'
  AND column_name = ANY($1::text[])`, []string{"lease_owner", "lease_token", "lease_expires_at"}).Scan(&count); err != nil {
		t.Fatalf("inspect finalization lease columns: %v", err)
	}
	if count != 3 {
		t.Fatalf("finalization lease columns=%d, want 3", count)
	}
}

func exerciseExternalPRFinalizationHistoricalStateConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
ALTER TABLE external_pr_reconcile_finalization
    DROP CONSTRAINT IF EXISTS external_pr_reconcile_finalization_state_check;
ALTER TABLE external_pr_reconcile_finalization
    ADD CONSTRAINT external_pr_reconcile_finalization_state_check
    CHECK (state IN ('pending', 'succeeded', 'recorded', 'dead'));`); err != nil {
		t.Fatalf("seed historical 306 state constraint: %v", err)
	}
}

func exerciseExternalPRFinalizationRetryStateRollback(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	serverRoot, tableName string,
	lockKey int64,
) {
	t.Helper()
	const (
		workspaceID       = "00000000-0000-4000-8000-000000000049"
		issueID           = "00000000-0000-4000-8000-000000000149"
		exhaustedID       = "00000000-0000-4000-8000-000000000951"
		availableID       = "00000000-0000-4000-8000-000000000952"
		retryAfterUpID    = "00000000-0000-4000-8000-000000000953"
		activityExhausted = "00000000-0000-4000-8000-000000000954"
		activityAvailable = "00000000-0000-4000-8000-000000000955"
		activityAfterUp   = "00000000-0000-4000-8000-000000000956"
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    state, attempt, max_attempts, next_attempt_at,
    lease_owner, lease_token, lease_expires_at
) VALUES
  ($1,$2,$3,'cc-retry-down-exhausted','migration','todo','done',$4,'{}','retry_wait',2,2,now(),'runner-a',$5,now()-interval '1 second'),
  ($6,$2,$3,'cc-retry-down-available','migration','todo','done',$7,'{}','retry_wait',1,2,now(),'runner-b',$8,now()-interval '1 second')`,
		exhaustedID, workspaceID, issueID, activityExhausted,
		"00000000-0000-4000-8000-000000000957",
		availableID, activityAvailable,
		"00000000-0000-4000-8000-000000000958"); err != nil {
		t.Fatalf("seed retry_wait rollback rows: %v", err)
	}

	down := filepath.Join(serverRoot, "migrations", "310_external_pr_reconcile_finalization_retry_state.down.sql")
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "down", Files: []string{down}, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
	}); err != nil {
		t.Fatalf("run 293 down with retry_wait rows: %v", err)
	}
	var exhaustedState, availableState string
	var exhaustedOwner, availableOwner, exhaustedToken, availableToken *string
	if err := pool.QueryRow(ctx, `
SELECT state, lease_owner, lease_token::text
FROM external_pr_reconcile_finalization WHERE id=$1`, exhaustedID).Scan(&exhaustedState, &exhaustedOwner, &exhaustedToken); err != nil {
		t.Fatalf("read exhausted retry rollback row: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT state, lease_owner, lease_token::text
FROM external_pr_reconcile_finalization WHERE id=$1`, availableID).Scan(&availableState, &availableOwner, &availableToken); err != nil {
		t.Fatalf("read available retry rollback row: %v", err)
	}
	if exhaustedState != "dead" || availableState != "pending" || exhaustedOwner != nil || availableOwner != nil || exhaustedToken != nil || availableToken != nil {
		t.Fatalf("293 down retry mapping=(%q,%q,%v,%v,%v,%v), want dead/pending and cleared leases", exhaustedState, availableState, exhaustedOwner, availableOwner, exhaustedToken, availableToken)
	}

	up := filepath.Join(serverRoot, "migrations", "310_external_pr_reconcile_finalization_retry_state.up.sql")
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: []string{up}, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
	}); err != nil {
		t.Fatalf("run 293 up after retry_wait rollback: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pr_reconcile_finalization (
    id, workspace_id, issue_id, source_revision, source,
    previous_status, terminal_status, status_activity_id, activity_ids,
    state, attempt, max_attempts, next_attempt_at
) VALUES ($1,$2,$3,'cc-retry-after-up','migration','todo','done',$4,'{}','retry_wait',0,2,now())`,
		retryAfterUpID, workspaceID, issueID, activityAfterUp); err != nil {
		t.Fatalf("293 up did not restore retry_wait: %v", err)
	}
}

func exerciseExternalPRReconcileIndexWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, spec := range externalPRReconcileIndexSpecs {
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong external PR reconcile index fixture %s: %v", spec.Name, err)
		}
	}
	for _, spec := range externalPRReconcileIndexSpecs {
		if err := reconcileMigrationIndex(spec)(ctx, pool); err != nil {
			t.Fatalf("repair wrong external PR reconcile index %s: %v", spec.Name, err)
		}
		if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
			t.Fatalf("wrong external PR reconcile index %s did not recover: exact=%v err=%v", spec.Name, exact, err)
		}
	}
}

func exerciseExternalPRReconcileIndexInvalidRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableFQN, tableName string, lockKey int64) {
	t.Helper()
	versions := []string{
		"303_external_pr_reconcile_work_identity_index",
		"304_external_pr_reconcile_work_claim_index",
		"305_external_pr_reconcile_work_issue_index",
	}
	for i, spec := range externalPRReconcileIndexSpecs {
		migration := 303 + i
		version := versions[i]
		files := realMigrationVersions(t, serverRoot, version)
		t.Run("external_pr_reconcile_"+strconv.Itoa(migration), func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration %d ledger fixture: %v", migration, err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
				t.Fatalf("drop migration %d exact index: %v", migration, err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, spec)
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err != nil {
				t.Fatalf("repair migration %d invalid concurrent artifact: %v", migration, err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
				t.Fatalf("migration %d invalid artifact did not recover: exact=%v err=%v", migration, exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration %d invalid-index fixture function: %v", migration, err)
			}
		})
	}
}

func exerciseExternalPRFinalizationIndexWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, spec := range externalPRFinalizationIndexSpecs {
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong external PR finalization index fixture %s: %v", spec.Name, err)
		}
	}
}

func exerciseExternalPRFinalizationIndexInvalidRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableFQN, tableName string, lockKey int64) {
	t.Helper()
	versions := []string{
		"307_external_pr_reconcile_finalization_work_index",
		"308_comment_external_pr_finalization_index",
	}
	for i, spec := range externalPRFinalizationIndexSpecs {
		migration := 307 + i
		version := versions[i]
		files := realMigrationVersions(t, serverRoot, version)
		t.Run("external_pr_finalization_"+strconv.Itoa(migration), func(t *testing.T) {
			if spec.Table == "external_pr_reconcile_finalization" {
				_, err := pool.Exec(ctx, `INSERT INTO external_pr_reconcile_finalization (workspace_id, issue_id, work_id, source_revision, source, previous_status, terminal_status, status_activity_id) VALUES ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149', '00000000-0000-4000-8000-000000000748', 'cc-finalization-index', 'migration', 'todo', 'done', '00000000-0000-4000-8000-000000000848') ON CONFLICT DO NOTHING`)
				if err != nil {
					t.Fatalf("seed finalization index row: %v", err)
				}
			} else {
				_, err := pool.Exec(ctx, `INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type, finalization_key) VALUES ('00000000-0000-4000-8000-000000000149', '00000000-0000-4000-8000-000000000049', 'system', '00000000-0000-0000-0000-000000000000', 'migration finalization index', 'system', '00000000-0000-4000-8000-000000000950') ON CONFLICT DO NOTHING`)
				if err != nil {
					t.Fatalf("seed comment finalization index row: %v", err)
				}
			}
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration %d ledger fixture: %v", migration, err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
				t.Fatalf("drop migration %d exact index: %v", migration, err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, spec)
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err != nil {
				t.Fatalf("repair migration %d invalid concurrent artifact: %v", migration, err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
				t.Fatalf("migration %d invalid artifact did not recover: exact=%v err=%v", migration, exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration %d invalid-index fixture function: %v", migration, err)
			}
		})
	}
}

func exerciseExternalPRInboxIndexWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, spec := range externalPRInboxIndexSpecs {
		if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS "+pgx.Identifier{spec.Name}.Sanitize()); err != nil {
			t.Fatalf("drop inbox delivery-key index fixture %s: %v", spec.Name, err)
		}
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong inbox delivery-key index fixture %s: %v", spec.Name, err)
		}
	}
}

func exerciseExternalPRInboxIndexLedgeredWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableName string, lockKey int64) {
	t.Helper()
	spec := externalPRInboxIndexSpecs[0]
	schema, exact, exists, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil || !exact || !exists {
		t.Fatalf("migration 312 ledgered fixture is not initially exact: schema=%q exact=%v exists=%v err=%v", schema, exact, exists, err)
	}
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
		t.Fatalf("drop ledgered migration 312 index: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
		t.Fatalf("create ledgered migration 312 wrong-definition fixture: %v", err)
	}
	files := realMigrationVersions(t, serverRoot, "312_inbox_item_delivery_key_index")
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		PostFenceReconcileHooks: postFenceReconciliationHooks,
	}); err != nil {
		t.Fatalf("repair ledgered migration 312 wrong-definition fixture: %v", err)
	}
	if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
		t.Fatalf("ledgered migration 312 did not converge to exact ready/valid index: exact=%v err=%v", exact, err)
	}
}

func exerciseExternalPRInboxIndexInvalidRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableFQN, tableName string, lockKey int64) {
	t.Helper()
	for _, spec := range externalPRInboxIndexSpecs {
		version := "312_inbox_item_delivery_key_index"
		files := realMigrationVersions(t, serverRoot, version)
		t.Run("external_pr_inbox_312", func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration 312 ledger fixture: %v", err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
				t.Fatalf("drop migration 312 exact index: %v", err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, spec)
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err != nil {
				t.Fatalf("repair migration 312 invalid concurrent artifact: %v", err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
				t.Fatalf("migration 312 invalid artifact did not recover: exact=%v err=%v", exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration 312 invalid-index fixture function: %v", err)
			}
		})
	}
}

func exerciseExternalPRPrimaryKeyIndexWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, spec := range externalPRPrimaryKeyIndexSpecs {
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong external PR primary-key index fixture %s: %v", spec.Name, err)
		}
	}
}

func exerciseExternalPRPrimaryKeyIndexInvalidRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableFQN, tableName string, lockKey int64) {
	t.Helper()
	versions := []string{
		"313_external_pr_reconcile_work_id_index",
		"314_external_pr_reconcile_finalization_id_index",
	}
	for i, spec := range externalPRPrimaryKeyIndexSpecs {
		migration := 313 + i
		version := versions[i]
		files := realMigrationVersions(t, serverRoot, version)
		t.Run("external_pr_primary_key_"+strconv.Itoa(migration), func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration %d ledger fixture: %v", migration, err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
				t.Fatalf("drop migration %d exact index: %v", migration, err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, spec)
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks,
			}); err != nil {
				t.Fatalf("repair migration %d invalid concurrent artifact: %v", migration, err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
				t.Fatalf("migration %d invalid artifact did not recover: exact=%v err=%v", migration, exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration %d invalid-index fixture function: %v", migration, err)
			}
		})
	}
}

func exerciseExternalPRPrimaryKeyLedgerGap(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableFQN, tableName string, lockKey int64) {
	t.Helper()
	versions := []string{
		"315_external_pr_reconcile_work_primary_key",
		"316_external_pr_reconcile_finalization_primary_key",
	}
	for migration := 315; migration <= 316; migration++ {
		version := versions[migration-315]
		files := realMigrationVersions(t, serverRoot, version)
		var beforeOID uint32
		if err := pool.QueryRow(ctx, `
SELECT c.conindid::oid
FROM pg_constraint c
WHERE c.conrelid = CASE $1::integer
    WHEN 315 THEN 'external_pr_reconcile_work'::regclass
    ELSE 'external_pr_reconcile_finalization'::regclass
END
  AND c.contype='p'`, migration).Scan(&beforeOID); err != nil {
			t.Fatalf("read attached primary-key authority before ledger-gap migration %d: %v", migration, err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", version); err != nil {
			t.Fatalf("simulate missing ledger for migration %d: %v", migration, err)
		}
		if err := runMigrations(ctx, pool, runOptions{
			Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
			Hooks: preMigrationHooks,
		}); err != nil {
			t.Fatalf("recover attached primary-key migration %d after ledger gap: %v", migration, err)
		}
		var afterOID uint32
		if err := pool.QueryRow(ctx, `
SELECT c.conindid::oid
FROM pg_constraint c
WHERE c.conrelid = CASE $1::integer
    WHEN 315 THEN 'external_pr_reconcile_work'::regclass
    ELSE 'external_pr_reconcile_finalization'::regclass
END
  AND c.contype='p'`, migration).Scan(&afterOID); err != nil {
			t.Fatalf("read attached primary-key authority after ledger-gap migration %d: %v", migration, err)
		}
		if afterOID != beforeOID {
			t.Fatalf("migration %d rebuilt attached primary-key index across ledger gap: before=%d after=%d", migration, beforeOID, afterOID)
		}
		var ledgerRows int
		if err := pool.QueryRow(ctx, "SELECT count(*)::int FROM "+tableFQN+" WHERE version=$1", version).Scan(&ledgerRows); err != nil {
			t.Fatalf("read repaired migration %d ledger: %v", migration, err)
		}
		if ledgerRows != 1 {
			t.Fatalf("migration %d ledger rows=%d, want one", migration, ledgerRows)
		}
	}
}

func externalPRPrimaryKeyMigrationVersion(migration int) string {
	switch migration {
	case 315:
		return "315_external_pr_reconcile_work_primary_key"
	case 316:
		return "316_external_pr_reconcile_finalization_primary_key"
	default:
		panic(fmt.Sprintf("unsupported External PR primary-key migration %d", migration))
	}
}

func exerciseExternalPRPrimaryKeyWrongAuthorityRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableName string, lockKey int64) {
	t.Helper()
	tests := []struct {
		migration  int
		table      string
		constraint string
		source     string
	}{
		{315, "external_pr_reconcile_work", "external_pr_reconcile_work_pkey", "external_pr_reconcile_work_id_uidx"},
		{316, "external_pr_reconcile_finalization", "external_pr_reconcile_finalization_pkey", "external_pr_reconcile_finalization_id_uidx"},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.migration)+"_source_wrong", func(t *testing.T) {
			t.Helper()
			tableIdent := pgx.Identifier{tc.table}.Sanitize()
			constraintIdent := pgx.Identifier{tc.constraint}.Sanitize()
			sourceIdent := pgx.Identifier{tc.source}.Sanitize()
			version := externalPRPrimaryKeyMigrationVersion(tc.migration)
			files := realMigrationVersions(t, serverRoot, version)

			if _, err := pool.Exec(ctx, "DELETE FROM "+tableIdent); err != nil {
				t.Fatalf("clear %s rows: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "ALTER TABLE "+tableIdent+" DROP CONSTRAINT IF EXISTS "+constraintIdent); err != nil {
				t.Fatalf("drop exact %s authority: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+sourceIdent+" ON "+tableIdent+" (workspace_id)"); err != nil {
				t.Fatalf("create wrong source authority for %s: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQNForContextContinuity(t, tableName)+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration %d ledger for wrong source: %v", tc.migration, err)
			}
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err == nil {
				t.Fatalf("migration %d accepted wrong source primary-key authority", tc.migration)
			}
			var ledgerRows int
			if err := pool.QueryRow(ctx, "SELECT count(*)::int FROM "+tableFQNForContextContinuity(t, tableName)+" WHERE version=$1", version).Scan(&ledgerRows); err != nil {
				t.Fatalf("read failed migration %d ledger: %v", tc.migration, err)
			}
			if ledgerRows != 0 {
				t.Fatalf("wrong source migration %d wrote %d ledger rows", tc.migration, ledgerRows)
			}
			var sourceDefinition string
			if err := pool.QueryRow(ctx, "SELECT pg_get_indexdef(to_regclass($1))", tc.source).Scan(&sourceDefinition); err != nil {
				t.Fatalf("read preserved wrong source authority %s: %v", tc.source, err)
			}
			if !strings.Contains(sourceDefinition, "workspace_id") {
				t.Fatalf("wrong source authority changed after failed migration %d: %q", tc.migration, sourceDefinition)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+sourceIdent); err != nil {
				t.Fatalf("drop wrong source authority %s: %v", tc.source, err)
			}
			if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+sourceIdent+" ON "+tableIdent+" (id)"); err != nil {
				t.Fatalf("restore exact source authority %s: %v", tc.source, err)
			}
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err != nil {
				t.Fatalf("restore migration %d after wrong source: %v", tc.migration, err)
			}
		})

		t.Run(strconv.Itoa(tc.migration)+"_target_wrong", func(t *testing.T) {
			t.Helper()
			tableIdent := pgx.Identifier{tc.table}.Sanitize()
			constraintIdent := pgx.Identifier{tc.constraint}.Sanitize()
			wrongIndexName := "cc_wrong_" + strconv.Itoa(tc.migration) + "_primary_key"
			wrongIndexIdent := pgx.Identifier{wrongIndexName}.Sanitize()
			correctSourceIdent := pgx.Identifier{tc.source}.Sanitize()
			version := externalPRPrimaryKeyMigrationVersion(tc.migration)
			files := realMigrationVersions(t, serverRoot, version)

			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQNForContextContinuity(t, tableName)+" WHERE version=$1", version); err != nil {
				t.Fatalf("remove migration %d ledger for wrong target: %v", tc.migration, err)
			}
			if _, err := pool.Exec(ctx, "ALTER TABLE "+tableIdent+" DROP CONSTRAINT IF EXISTS "+constraintIdent); err != nil {
				t.Fatalf("drop exact target authority %s: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+wrongIndexIdent+" ON "+tableIdent+" (workspace_id)"); err != nil {
				t.Fatalf("create wrong target index for %s: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "ALTER TABLE "+tableIdent+" ADD CONSTRAINT "+constraintIdent+" PRIMARY KEY USING INDEX "+wrongIndexIdent); err != nil {
				t.Fatalf("attach wrong target authority %s: %v", tc.table, err)
			}
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err == nil {
				t.Fatalf("migration %d accepted wrong target primary-key authority", tc.migration)
			}
			var ledgerRows int
			if err := pool.QueryRow(ctx, "SELECT count(*)::int FROM "+tableFQNForContextContinuity(t, tableName)+" WHERE version=$1", version).Scan(&ledgerRows); err != nil {
				t.Fatalf("read failed migration %d target ledger: %v", tc.migration, err)
			}
			if ledgerRows != 0 {
				t.Fatalf("wrong target migration %d wrote %d ledger rows", tc.migration, ledgerRows)
			}
			var targetDefinition string
			if err := pool.QueryRow(ctx, "SELECT pg_get_indexdef(c.conindid) FROM pg_constraint c WHERE c.conrelid=$1::regclass AND c.conname=$2", tc.table, tc.constraint).Scan(&targetDefinition); err != nil {
				t.Fatalf("read preserved wrong target authority %s: %v", tc.table, err)
			}
			if !strings.Contains(targetDefinition, "workspace_id") {
				t.Fatalf("wrong target authority changed after failed migration %d: %q", tc.migration, targetDefinition)
			}
			if _, err := pool.Exec(ctx, "ALTER TABLE "+tableIdent+" DROP CONSTRAINT IF EXISTS "+constraintIdent); err != nil {
				t.Fatalf("drop preserved wrong target authority %s: %v", tc.table, err)
			}
			if _, err := pool.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+correctSourceIdent+" ON "+tableIdent+" (id)"); err != nil {
				t.Fatalf("restore exact source authority %s: %v", tc.source, err)
			}
			if err := runMigrations(ctx, pool, runOptions{Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks}); err != nil {
				t.Fatalf("restore migration %d after wrong target: %v", tc.migration, err)
			}
		})
	}
}

func tableFQNForContextContinuity(t *testing.T, tableName string) string {
	t.Helper()
	parts := strings.Split(tableName, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected schema migrations table %q", tableName)
	}
	return pgx.Identifier{parts[0], parts[1]}.Sanitize()
}

func exerciseExternalPRPrimaryKeyDownUp(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableName string, lockKey int64) {
	t.Helper()
	downFiles := []string{
		filepath.Join(serverRoot, "migrations", "316_external_pr_reconcile_finalization_primary_key.down.sql"),
		filepath.Join(serverRoot, "migrations", "315_external_pr_reconcile_work_primary_key.down.sql"),
		filepath.Join(serverRoot, "migrations", "314_external_pr_reconcile_finalization_id_index.down.sql"),
		filepath.Join(serverRoot, "migrations", "313_external_pr_reconcile_work_id_index.down.sql"),
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "down", Files: downFiles, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
	}); err != nil {
		t.Fatalf("primary-key migration down: %v", err)
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: realMigrationVersions(t, serverRoot,
			"313_external_pr_reconcile_work_id_index",
			"314_external_pr_reconcile_finalization_id_index",
			"315_external_pr_reconcile_work_primary_key",
			"316_external_pr_reconcile_finalization_primary_key",
		), SchemaMigrationsTable: tableName,
		AdvisoryLockKey: lockKey, Hooks: preMigrationHooks,
	}); err != nil {
		t.Fatalf("primary-key migration up after down: %v", err)
	}
}

func assertExternalPRPrimaryKeyConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, tc := range []struct {
		table      string
		constraint string
		index      string
	}{
		{table: "external_pr_reconcile_work", constraint: "external_pr_reconcile_work_pkey", index: "external_pr_reconcile_work_pkey"},
		{table: "external_pr_reconcile_finalization", constraint: "external_pr_reconcile_finalization_pkey", index: "external_pr_reconcile_finalization_pkey"},
	} {
		var count int
		var indexName string
		if err := pool.QueryRow(ctx, `
SELECT count(*)::int, COALESCE(max(index_rel.relname), '')
FROM pg_constraint c
JOIN pg_class index_rel ON index_rel.oid = c.conindid
WHERE c.conrelid=$1::regclass AND c.contype='p' AND c.conname=$2`, tc.table, tc.constraint).Scan(&count, &indexName); err != nil {
			t.Fatalf("inspect primary key %s: %v", tc.constraint, err)
		}
		if count != 1 || indexName != tc.index {
			t.Fatalf("primary key %s=(count=%d,index=%q), want (1,%q)", tc.constraint, count, indexName, tc.index)
		}
	}
}

func exerciseCleanupIndexRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// Simulate wrong same-name artifacts for both cleanup indexes. Their ordinary
	// pre-hooks must repair them before the corresponding migration is ledgered.
	for _, spec := range externalPRCleanupIndexSpecs {
		if _, err := pool.Exec(ctx, "DROP INDEX IF EXISTS "+pgx.Identifier{spec.Name}.Sanitize()); err != nil {
			t.Fatalf("drop cleanup index fixture %s: %v", spec.Name, err)
		}
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong cleanup index fixture %s: %v", spec.Name, err)
		}
	}
}

func exerciseCleanupIndexInvalidRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	serverRoot, tableFQN, tableName string,
	lockKey int64,
) {
	t.Helper()
	// Reproduce a real failed CREATE INDEX CONCURRENTLY artifact for each cleanup
	// migration, then simulate SQL-success/ledger-failure. Each ordinary pre-hook
	// must remove the invalid catalog entry before the exact migration is retried.
	tests := []struct {
		migration int
		version   string
		spec      migrationIndexSpec
	}{
		{283, "283_external_pr_link_issue_updated_index", externalPRCleanupIndexSpecs[0]},
		{284, "284_external_pr_receipt_issue_cleanup_index", externalPRCleanupIndexSpecs[1]},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.migration), func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", tc.version); err != nil {
				t.Fatalf("remove migration %d ledger fixture: %v", tc.migration, err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			indexName := pgx.Identifier{schema, tc.spec.Name}.Sanitize()
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
				t.Fatalf("drop migration %d exact index: %v", tc.migration, err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, tc.spec)
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: realMigrationVersions(t, serverRoot, tc.version),
				SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks,
			}); err != nil {
				t.Fatalf("repair migration %d invalid concurrent artifact: %v", tc.migration, err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, tc.spec); err != nil || !exact {
				t.Fatalf("migration %d invalid artifact did not recover: exact=%v err=%v", tc.migration, exact, err)
			}
			var ledgerRows int
			if err := pool.QueryRow(ctx, "SELECT COUNT(*)::int FROM "+tableFQN+" WHERE version=$1", tc.version).Scan(&ledgerRows); err != nil {
				t.Fatalf("read migration %d repaired ledger: %v", tc.migration, err)
			}
			if ledgerRows != 1 {
				t.Fatalf("migration %d repaired ledger rows=%d, want 1", tc.migration, ledgerRows)
			}
			ledgerAfterRepair := migrationLedgerSnapshot(t, ctx, pool, tableFQN)
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: realMigrationVersions(t, serverRoot, tc.version),
				SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks,
			}); err != nil {
				t.Fatalf("rerun migration %d after invalid recovery: %v", tc.migration, err)
			}
			if ledgerAfterRerun := migrationLedgerSnapshot(t, ctx, pool, tableFQN); !reflect.DeepEqual(ledgerAfterRerun, ledgerAfterRepair) {
				t.Fatalf("migration %d ledger changed on rerun\nafter repair=%v\nafter rerun=%v", tc.migration, ledgerAfterRepair, ledgerAfterRerun)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, tc.spec); err != nil || !exact {
				t.Fatalf("migration %d exact index changed on rerun: exact=%v err=%v", tc.migration, exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration %d invalid-index fixture function: %v", tc.migration, err)
			}
		})
	}
}

func seedPRMergeIndexRecoveryRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO workload_pr_merge_delegation (
    id, workspace_id, issue_id, external_pr_link_id, task_id, execution_id, runtime_id,
    target_instance, canonical_repository_id, canonical_repository,
    provider_binding_id, provider_binding_revision, provider_repository,
    ags_pr_number, provider_pr_number, expected_head_sha, expected_base_sha,
    base_ref, merge_method, projection_facts_revision, facts_digest, state
) VALUES (
    '00000000-0000-4000-8000-000000000744',
    '00000000-0000-4000-8000-000000000049',
    '00000000-0000-4000-8000-000000000149',
    '00000000-0000-4000-8000-000000000344',
    '00000000-0000-4000-8000-000000000444',
    '00000000-0000-4000-8000-000000000544',
    '00000000-0000-4000-8000-000000000644',
    'mini',
    'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    'jackie/agent-kit',
    'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
    'jackie/agent-kit', 41, 52,
    '1111111111111111111111111111111111111111',
    '2222222222222222222222222222222222222222',
    'main', 'rebase',
    'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
    'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
    'pending_approval'
);
INSERT INTO workload_pr_merge_delegation_event (
    id, workspace_id, issue_id, delegation_id, event_type, actor_type, actor_id
) VALUES (
    '00000000-0000-4000-8000-000000000844',
    '00000000-0000-4000-8000-000000000049',
    '00000000-0000-4000-8000-000000000149',
    '00000000-0000-4000-8000-000000000744',
    'request_created', 'system', 'migration-recovery-test'
)`); err != nil {
		t.Fatalf("seed delegated merge index recovery rows: %v", err)
	}
}

func exercisePRMergeIndexWrongDefinitionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, spec := range prMergeDelegationIndexSpecs {
		if _, err := pool.Exec(ctx, "CREATE INDEX "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{spec.Table}.Sanitize()+"("+pgx.Identifier{spec.Columns[0]}.Sanitize()+")"); err != nil {
			t.Fatalf("create wrong delegated merge index fixture %s: %v", spec.Name, err)
		}
	}
}

func exercisePRMergeIndexInvalidRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	serverRoot, tableFQN, tableName string,
	lockKey int64,
) {
	t.Helper()
	versions := []string{
		"286_workload_pr_merge_delegation_id_index",
		"287_workload_pr_merge_delegation_active_index",
		"288_workload_pr_merge_delegation_consumer_intent_index",
		"289_workload_pr_merge_delegation_issue_state_index",
		"290_workload_pr_merge_delegation_event_id_index",
		"291_workload_pr_merge_delegation_event_history_index",
	}
	for i, spec := range prMergeDelegationIndexSpecs {
		migration := 286 + i
		t.Run("delegated_merge_"+strconv.Itoa(migration), func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", versions[i]); err != nil {
				t.Fatalf("remove migration %d ledger fixture: %v", migration, err)
			}
			schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+pgx.Identifier{schema, spec.Name}.Sanitize()); err != nil {
				t.Fatalf("drop migration %d exact index: %v", migration, err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, spec)
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: realMigrationVersions(t, serverRoot, versions[i]),
				SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey, Hooks: preMigrationHooks,
			}); err != nil {
				t.Fatalf("repair migration %d invalid concurrent artifact: %v", migration, err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || !exact {
				t.Fatalf("migration %d invalid artifact did not recover: exact=%v err=%v", migration, exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop migration %d invalid-index fixture function: %v", migration, err)
			}
		})
	}
}

func realMigrationVersions(t *testing.T, serverRoot string, versions ...string) []string {
	t.Helper()
	files := make([]string, 0, len(versions))
	for _, version := range versions {
		path := filepath.Join(serverRoot, "migrations", version+".up.sql")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("required migration %s: %v", version, err)
		}
		files = append(files, path)
	}
	return files
}

func realMigrationRange(t *testing.T, serverRoot string, first, last int) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(serverRoot, "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var selected []string
	for _, path := range files {
		base := filepath.Base(path)
		prefix := strings.SplitN(base, "_", 2)[0]
		number, err := strconv.Atoi(prefix)
		if err == nil && number >= first && number <= last {
			selected = append(selected, path)
		}
	}
	sort.Strings(selected)
	if len(selected) == 0 {
		t.Fatalf("no real migrations in range %03d-%03d", first, last)
	}
	return selected
}

func globMigrationFiles(t *testing.T, pattern string) []string {
	t.Helper()
	files, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatalf("no migration fixtures for %s", pattern)
	}
	return files
}

func exerciseMigration273Recovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tableFQN, tableName string,
	lockKey int64,
	migration273 []string,
	scenario string,
) {
	t.Helper()
	const version = "280_external_pr_link_workspace_idempotency_index"

	if scenario == "clean_v0_4_12" {
		const duplicateKey = "cc-v049-invalid-index-build"
		if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number,
    link_confidence, completion_intent, state, idempotency_key
) VALUES
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/invalid-index-one', 4701, 'authoritative', TRUE, 'open', $1),
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/invalid-index-two', 4702, 'authoritative', TRUE, 'open', $1)`, duplicateKey); err != nil {
			t.Fatalf("seed failed-index duplicate: %v", err)
		}
		err := runMigrations(ctx, pool, runOptions{
			Direction: "up", Files: migration273, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		})
		if err == nil {
			t.Fatal("migration 273 unexpectedly accepted duplicate workspace keys")
		}
		var ready, valid bool
		if scanErr := pool.QueryRow(ctx, `
SELECT i.indisready, i.indisvalid FROM pg_index i
JOIN pg_class c ON c.oid=i.indexrelid
WHERE c.oid=to_regclass('idx_external_pr_link_workspace_idempotency')`).Scan(&ready, &valid); scanErr != nil {
			t.Fatalf("load failed concurrent index artifact: %v", scanErr)
		}
		if ready || valid {
			t.Fatalf("failed concurrent index ready=%v valid=%v, want false/false", ready, valid)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM external_pull_request_link WHERE external_repo='jackie/invalid-index-two'`); err != nil {
			t.Fatalf("remove duplicate before supported retry: %v", err)
		}
	} else {
		if scenario == "fork_v0_4_9" {
			if _, err := pool.Exec(ctx, `DROP INDEX CONCURRENTLY idx_external_pr_link_workspace_idempotency`); err != nil {
				t.Fatalf("drop v0.4.9 workspace idempotency index fixture: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `CREATE INDEX CONCURRENTLY idx_external_pr_link_workspace_idempotency ON external_pull_request_link(external_repo)`); err != nil {
			t.Fatalf("create wrong-definition migration artifact: %v", err)
		}
		// Reproduce the historical IF NOT EXISTS bug: an old runner accepts and
		// ledgers the wrong same-name index. The current runner must reconcile it
		// before honoring that ledger skip.
		if err := runMigrations(ctx, pool, runOptions{
			Direction: "up", Files: migration273, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		}); err != nil {
			t.Fatalf("simulate historical ledgered wrong-definition index: %v", err)
		}
	}

	var ledgered bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+tableFQN+" WHERE version=$1)", version).Scan(&ledgered); err != nil {
		t.Fatalf("read migration 273 ledger after failed apply: %v", err)
	}
	if scenario == "clean_v0_4_12" && ledgered {
		t.Fatal("failed concurrent migration 273 was ledgered")
	}
	if scenario != "clean_v0_4_12" && !ledgered {
		t.Fatal("historical wrong-definition migration 273 fixture was not ledgered")
	}
}

func exerciseLedgeredIndexRecovery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	serverRoot, tableFQN, tableName string,
	lockKey int64,
) {
	t.Helper()
	versions := []struct {
		number  int
		version string
		spec    migrationIndexSpec
	}{
		{273, "273_external_pr_link_id_unique_index", externalPRIndexSpecs[0]},
		{274, "274_external_pr_link_identity_index", externalPRIndexSpecs[1]},
		{275, "275_external_pr_link_issue_state_index", externalPRIndexSpecs[2]},
		{276, "276_external_pr_link_idempotency_index", externalPRIndexSpecs[3]},
		// Authority index still exists when this recovery runs (before T016 292-299).
		{278, "278_workspace_workload_authority_workspace_id_index", externalPRIndexSpecs[4]},
		{280, "280_external_pr_link_workspace_idempotency_index", externalPRIndexSpecs[5]},
	}
	for _, tc := range versions {
		t.Run("recovery_"+tc.version, func(t *testing.T) {
			files := realMigrationVersions(t, serverRoot, tc.version)
			var oidBefore uint32
			if err := pool.QueryRow(ctx, `SELECT $1::regclass::oid`, tc.spec.Name).Scan(&oidBefore); err != nil {
				t.Fatalf("read exact index oid: %v", err)
			}
			if _, err := pool.Exec(ctx, "DELETE FROM "+tableFQN+" WHERE version=$1", tc.version); err != nil {
				t.Fatalf("simulate SQL-success/ledger-failure: %v", err)
			}
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
				ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
			}); err != nil {
				t.Fatalf("recover SQL-success/ledger-failure: %v", err)
			}
			var oidAfter uint32
			if err := pool.QueryRow(ctx, `SELECT $1::regclass::oid`, tc.spec.Name).Scan(&oidAfter); err != nil {
				t.Fatal(err)
			}
			if oidAfter != oidBefore {
				t.Fatalf("valid exact index was rebuilt across ledger recovery: before=%d after=%d", oidBefore, oidAfter)
			}

			schema, _, _, err := inspectMigrationIndex(ctx, pool, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			indexName := pgx.Identifier{schema, tc.spec.Name}.Sanitize()
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
				t.Fatalf("drop exact index for ledgered-wrong fixture: %v", err)
			}
			wrongSQL := "CREATE INDEX CONCURRENTLY " + pgx.Identifier{tc.spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, tc.spec.Table}.Sanitize() + "(" + pgx.Identifier{tc.spec.Columns[0]}.Sanitize() + ")"
			if tc.spec.PredicateNormalized != "" {
				verb := "CREATE INDEX CONCURRENTLY "
				if tc.spec.Unique {
					verb = "CREATE UNIQUE INDEX CONCURRENTLY "
				}
				columns := make([]string, len(tc.spec.Columns))
				for i, column := range tc.spec.Columns {
					columns[i] = pgx.Identifier{column}.Sanitize()
				}
				// Same table/key shape but deliberately missing the predicate proves
				// the recovery gate checks predicate semantics, not only names.
				wrongSQL = verb + pgx.Identifier{tc.spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, tc.spec.Table}.Sanitize() + "(" + strings.Join(columns, ", ") + ")"
			}
			if _, err := pool.Exec(ctx, wrongSQL); err != nil {
				t.Fatalf("create ledgered wrong-definition fixture: %v", err)
			}
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
				ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
			}); err != nil {
				t.Fatalf("repair ledgered wrong-definition index: %v", err)
			}
			_, exact, _, err := inspectMigrationIndex(ctx, pool, tc.spec)
			if err != nil || !exact {
				t.Fatalf("ledgered wrong-definition index did not recover: exact=%v err=%v", exact, err)
			}

			// An INCLUDE column appears in pg_index.indkey but is not part of
			// the unique key. The exact checker must reject and repair it.
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
				t.Fatalf("drop exact index for INCLUDE fixture: %v", err)
			}
			if _, err := pool.Exec(ctx, createIncludeWrongIndexSQL(schema, tc.spec)); err != nil {
				t.Fatalf("create INCLUDE-shaped wrong index: %v", err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, tc.spec); err != nil || exact {
				t.Fatalf("INCLUDE-shaped index accepted as exact: exact=%v err=%v", exact, err)
			}
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
				ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
			}); err != nil {
				t.Fatalf("repair INCLUDE-shaped index: %v", err)
			}

			// Reproduce a real failed CREATE INDEX CONCURRENTLY artifact for
			// every catalog authority, including the non-unique issue-state index.
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
				t.Fatalf("drop exact index for invalid fixture: %v", err)
			}
			failFunction := createFailedConcurrentIndexArtifact(t, ctx, pool, schema, tc.spec)
			if err := runMigrations(ctx, pool, runOptions{
				Direction: "up", Files: files, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
				ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
			}); err != nil {
				t.Fatalf("repair ledgered invalid concurrent index: %v", err)
			}
			if _, exact, _, err := inspectMigrationIndex(ctx, pool, tc.spec); err != nil || !exact {
				t.Fatalf("ledgered invalid index did not recover: exact=%v err=%v", exact, err)
			}
			if _, err := pool.Exec(ctx, "DROP FUNCTION "+failFunction+"(uuid)"); err != nil {
				t.Fatalf("drop invalid-index fixture function: %v", err)
			}
		})
	}

	// Simulate the historical IF NOT EXISTS bug exactly on origins where no
	// legacy global index blocks the duplicate fixture. The other origins already
	// exercise ledgered wrong definitions while retaining legacy authority until
	// migration 274.
	var legacyIndexExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('idx_external_pr_link_idempotency') IS NOT NULL`).Scan(&legacyIndexExists); err != nil {
		t.Fatal(err)
	}
	if legacyIndexExists {
		return
	}
	spec := externalPRIndexSpecs[5]
	schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil {
		t.Fatal(err)
	}
	indexName := pgx.Identifier{schema, spec.Name}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
		t.Fatal(err)
	}
	const duplicateKey = "cc-v0412-ledgered-invalid"
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number,
    link_confidence, completion_intent, state, idempotency_key
) VALUES
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/ledgered-invalid-one', 4811, 'authoritative', TRUE, 'open', $1),
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/ledgered-invalid-two', 4812, 'authoritative', TRUE, 'open', $1)`, duplicateKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, createMigrationIndexSQL(schema, spec)); err == nil {
		t.Fatal("ledgered invalid fixture unexpectedly built unique index")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM external_pull_request_link WHERE external_repo='jackie/ledgered-invalid-two'`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: realMigrationVersions(t, serverRoot, "280_external_pr_link_workspace_idempotency_index"), SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	}); err != nil {
		t.Fatalf("repair ledgered invalid 280 index: %v", err)
	}
	_, exact, _, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil || !exact {
		t.Fatalf("ledgered invalid index did not recover: exact=%v err=%v", exact, err)
	}
}

func exerciseMigration274Gate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableName string, lockKey int64) {
	t.Helper()
	spec := externalPRIndexSpecs[5]
	schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil {
		t.Fatal(err)
	}
	indexName := pgx.Identifier{schema, spec.Name}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE INDEX CONCURRENTLY "+pgx.Identifier{spec.Name}.Sanitize()+" ON "+pgx.Identifier{schema, spec.Table}.Sanitize()+"(external_repo)"); err != nil {
		t.Fatal(err)
	}
	err = runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: realMigrationVersions(t, serverRoot, "281_external_pr_legacy_idempotency_index_remove"), SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	})
	if err == nil || !strings.Contains(err.Error(), "refuse legacy idempotency index removal") {
		t.Fatalf("migration 281 did not fail closed on wrong 280 authority: %v", err)
	}
	if repairErr := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: realMigrationVersions(t, serverRoot, "280_external_pr_link_workspace_idempotency_index"), SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	}); repairErr != nil {
		t.Fatalf("repair 280 after 281 gate: %v", repairErr)
	}
}

func createIncludeWrongIndexSQL(schema string, spec migrationIndexSpec) string {
	verb := "CREATE INDEX CONCURRENTLY "
	if spec.Unique {
		verb = "CREATE UNIQUE INDEX CONCURRENTLY "
	}
	columns := make([]string, len(spec.Columns))
	for i, column := range spec.Columns {
		columns[i] = pgx.Identifier{column}.Sanitize()
	}
	includeColumn := "id"
	switch spec.Table {
	case "external_pull_request_receipt":
		includeColumn = "payload_hash"
	case "workspace_workload_authority":
		includeColumn = "team_identity_id"
	default:
		if spec.Name == "idx_external_pr_link_id" {
			includeColumn = "workspace_id"
		}
	}
	sql := verb + pgx.Identifier{spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, spec.Table}.Sanitize() +
		"(" + strings.Join(columns, ", ") + ") INCLUDE (" + pgx.Identifier{includeColumn}.Sanitize() + ")"
	if spec.Name == "idx_external_pr_link_issue_state" {
		sql += " WHERE state IN ('open', 'draft') AND link_confidence = 'authoritative'"
	}
	if spec.Name == "idx_external_pr_link_workspace_idempotency" {
		sql += " WHERE idempotency_key IS NOT NULL"
	}
	return sql
}

func createFailedConcurrentIndexArtifact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, spec migrationIndexSpec) string {
	t.Helper()
	if spec.Table == "external_pull_request_receipt" {
		if _, err := pool.Exec(ctx, `INSERT INTO external_pull_request_receipt (
workspace_id, idempotency_key, payload_hash, issue_id, provider, external_repo, external_number
) VALUES (
'00000000-0000-4000-8000-000000000049', 'cc-invalid-index-receipt', 'cc-invalid-index-payload',
'00000000-0000-4000-8000-000000000149', 'ags', 'jackie/invalid-index-receipt', 4999
) ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("seed receipt for failed concurrent build: %v", err)
		}
	}
	if spec.Table == "external_pr_reconcile_work" {
		if _, err := pool.Exec(ctx, `INSERT INTO external_pr_reconcile_work (
workspace_id, issue_id, link_id, kind, provider, external_repo, external_number, source_revision
)
SELECT
'00000000-0000-4000-8000-000000000050', '00000000-0000-4000-8000-000000000150',
'00000000-0000-4000-8000-000000000250', 'external_pr_terminal', 'ags',
'jackie/invalid-index-work', 5000, 'cc-invalid-index-work'
WHERE NOT EXISTS (
    SELECT 1 FROM external_pr_reconcile_work WHERE source_revision='cc-invalid-index-work'
)`); err != nil {
			t.Fatalf("seed reconcile work for failed concurrent build: %v", err)
		}
	}
	if spec.Table == "inbox_item" {
		if _, err := pool.Exec(ctx, `INSERT INTO inbox_item (
workspace_id, recipient_type, recipient_id, type, title, delivery_key
)
SELECT
'00000000-0000-4000-8000-000000000049', 'member',
'00000000-0000-4000-8000-000000000449', 'external_pr_terminal',
'Concurrent index failure fixture', 'cc-invalid-index-inbox'
WHERE NOT EXISTS (
    SELECT 1 FROM inbox_item WHERE delivery_key='cc-invalid-index-inbox'
)`); err != nil {
			t.Fatalf("seed inbox item for failed concurrent build: %v", err)
		}
	}
	functionName := "cc_fail_" + strings.TrimPrefix(spec.Name, "idx_")
	functionIdent := pgx.Identifier{schema, functionName}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE FUNCTION "+functionIdent+"(uuid) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$ BEGIN RAISE EXCEPTION 'intentional concurrent index failure'; END $$"); err != nil {
		t.Fatalf("create invalid-index fixture function: %v", err)
	}
	verb := "CREATE INDEX CONCURRENTLY "
	if spec.Unique {
		verb = "CREATE UNIQUE INDEX CONCURRENTLY "
	}
	columns := make([]string, len(spec.Columns))
	for i, column := range spec.Columns {
		columns[i] = pgx.Identifier{column}.Sanitize()
	}
	sql := verb + pgx.Identifier{spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, spec.Table}.Sanitize() +
		"(" + strings.Join(columns, ", ") + ") WHERE " + functionIdent + "(workspace_id)"
	if _, err := pool.Exec(ctx, sql); err == nil {
		t.Fatal("failed-index fixture unexpectedly succeeded")
	}
	var ready, valid bool
	if err := pool.QueryRow(ctx, `SELECT i.indisready, i.indisvalid FROM pg_index i WHERE i.indexrelid=to_regclass($1)`, schema+"."+spec.Name).Scan(&ready, &valid); err != nil {
		t.Fatalf("inspect failed concurrent artifact: %v", err)
	}
	if ready || valid {
		t.Fatalf("failed concurrent artifact ready=%v valid=%v", ready, valid)
	}
	return functionIdent
}

func prepareReconciliationFenceRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	spec := externalPRIndexSpecs[1]
	schema, _, _, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil {
		t.Fatal(err)
	}
	indexName := pgx.Identifier{schema, spec.Name}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, createIncludeWrongIndexSQL(schema, spec)); err != nil {
		t.Fatal(err)
	}
}

func assertReconciliationFenceStopsHistoricalRepair(t *testing.T, ctx context.Context, pool *pgxpool.Pool, serverRoot, tableName string, lockKey int64) {
	t.Helper()
	spec := externalPRIndexSpecs[1]
	schema, exact, _, err := inspectMigrationIndex(ctx, pool, spec)
	if err != nil || !exact {
		t.Fatalf("fence did not repair pre-ledger wrong definition: exact=%v err=%v", exact, err)
	}
	indexName := pgx.Identifier{schema, spec.Name}.Sanitize()
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
		t.Fatal(err)
	}
	futureSQL := "CREATE UNIQUE INDEX CONCURRENTLY " + pgx.Identifier{spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, spec.Table}.Sanitize() + "(workspace_id, provider, external_repo, external_number) INCLUDE (id)"
	if _, err := pool.Exec(ctx, futureSQL); err != nil {
		t.Fatal(err)
	}
	downFence := filepath.Join(serverRoot, "migrations", "282_external_pr_index_reconciliation_fence.down.sql")
	err = runMigrations(ctx, pool, runOptions{
		Direction: "down", Files: []string{downFence}, SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	})
	if err == nil || !strings.Contains(err.Error(), "refuse rollback across forward-only reconciliation fence") {
		t.Fatalf("down crossed reconciliation fence: %v", err)
	}
	ledgerIdent, quoteErr := quoteQualifiedIdentifier(tableName)
	if quoteErr != nil {
		t.Fatal(quoteErr)
	}
	var fenceLedgered bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+ledgerIdent+` WHERE version=$1)`, externalPRIndexReconciliationFenceVersion).Scan(&fenceLedgered); err != nil {
		t.Fatal(err)
	}
	if !fenceLedgered {
		t.Fatal("rejected down removed migration 282 ledger")
	}
	if err := runMigrations(ctx, pool, runOptions{
		Direction: "up", Files: realMigrationRange(t, serverRoot, 273, 282), SchemaMigrationsTable: tableName, AdvisoryLockKey: lockKey,
		ReconcileHooks: reconciliationMigrationHooks, ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	}); err != nil {
		t.Fatalf("post-fence migration rerun: %v", err)
	}
	if _, exact, _, err := inspectMigrationIndex(ctx, pool, spec); err != nil || exact {
		t.Fatalf("historical hook restored definition after fence: exact=%v err=%v", exact, err)
	}
	// Restore the accepted catalog for the remainder of this fixture. This is
	// test-only cleanup after proving the old hook stayed disabled.
	if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, createMigrationIndexSQL(schema, spec)); err != nil {
		t.Fatal(err)
	}
}

func seedCurrentBaseRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO "user" (id, name, email)
VALUES ('00000000-0000-4000-8000-000000000449', 'Context continuity user', 'cc-v049@example.invalid');
INSERT INTO workspace (id, name, slug, description, issue_prefix)
VALUES
  ('00000000-0000-4000-8000-000000000049', 'Context continuity fixture', 'cc-v049', 'migration fixture', 'CCV'),
  ('00000000-0000-4000-8000-000000000050', 'Context continuity second workspace', 'cc-v049-second', 'workspace idempotency fixture', 'CCW');
INSERT INTO member (id, workspace_id, user_id, role)
VALUES ('00000000-0000-4000-8000-000000000249', '00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000449', 'owner');
INSERT INTO issue (id, workspace_id, number, title, status, priority, position, creator_type, creator_id)
VALUES
  ('00000000-0000-4000-8000-000000000149', '00000000-0000-4000-8000-000000000049', 1,
   'Context continuity issue', 'in_progress', 'none', 1, 'member', '00000000-0000-4000-8000-000000000449'),
  ('00000000-0000-4000-8000-000000000150', '00000000-0000-4000-8000-000000000050', 1,
   'Context continuity second issue', 'in_progress', 'none', 1, 'member', '00000000-0000-4000-8000-000000000449');
`); err != nil {
		t.Fatalf("seed exact current base rows: %v", err)
	}
}

func seedLegacyExternalPRRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    id, workspace_id, issue_id, provider, external_repo, external_number,
    link_confidence, completion_intent, state, idempotency_key
) VALUES (
    '00000000-0000-4000-8000-000000000349',
    '00000000-0000-4000-8000-000000000049',
    '00000000-0000-4000-8000-000000000149',
    'ags', 'jackie/agent-kit', 49, 'authoritative', TRUE, 'merged',
    'cc-v049-legacy-fact'
)`); err != nil {
		t.Fatalf("seed legacy external PR row: %v", err)
	}
}

func assertWorkspaceIdempotencyIntermediate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, scenario string) {
	t.Helper()
	var ready, valid bool
	if err := pool.QueryRow(ctx, `
SELECT i.indisready, i.indisvalid
FROM pg_index i
JOIN pg_class c ON c.oid=i.indexrelid
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname='idx_external_pr_link_workspace_idempotency'`, schema).Scan(&ready, &valid); err != nil {
		t.Fatalf("workspace idempotency authority missing after 266 in %s: %v", scenario, err)
	}
	if !ready || !valid {
		t.Fatalf("workspace idempotency authority after 266 in %s ready=%v valid=%v", scenario, ready, valid)
	}

	const key = "cc-v049-intermediate-same-workspace-key"
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number,
    link_confidence, completion_intent, state, idempotency_key
) VALUES
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/intermediate-one', 491, 'authoritative', TRUE, 'open', $1)`, key); err != nil {
		t.Fatalf("seed intermediate workspace key in %s: %v", scenario, err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_link (
    workspace_id, issue_id, provider, external_repo, external_number,
    link_confidence, completion_intent, state, idempotency_key
) VALUES
    ('00000000-0000-4000-8000-000000000049', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/intermediate-two', 492, 'authoritative', TRUE, 'open', $1)`, key); err == nil {
		t.Fatalf("same-workspace duplicate key was accepted after 266 in %s", scenario)
	}
}

func assertWorkspaceScopedIdempotency(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// T017: receipt owns idempotency; same key may exist in two workspaces on the receipt table.
	const key = "cc-v049-shared-workspace-key"
	if _, err := pool.Exec(ctx, `
INSERT INTO external_pull_request_receipt (
    workspace_id, idempotency_key, payload_hash, issue_id, provider, external_repo, external_number
) VALUES
    ('00000000-0000-4000-8000-000000000049', $1, 'hash-one', '00000000-0000-4000-8000-000000000149',
     'ags', 'jackie/workspace-one', 501),
    ('00000000-0000-4000-8000-000000000050', $1, 'hash-two', '00000000-0000-4000-8000-000000000150',
     'ags', 'jackie/workspace-two', 502)`, key); err != nil {
		t.Fatalf("workspace-scoped receipt idempotency regression: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM external_pull_request_receipt WHERE idempotency_key=$1`, key).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("workspace-scoped receipt idempotency rows=%d, want 2", rows)
	}
}

func migrationLedgerSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableFQN string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, "SELECT version || '|' || applied_at::text FROM "+tableFQN+" ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func contextContinuityCatalogSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT kind || '|' || name || '|' || definition
FROM (
    SELECT 'index' AS kind, c.relname AS name,
           pg_get_indexdef(c.oid) || '|ready=' || i.indisready::text || '|valid=' || i.indisvalid::text AS definition
    FROM pg_class c
    JOIN pg_namespace n ON n.oid=c.relnamespace
    JOIN pg_index i ON i.indexrelid=c.oid
    WHERE n.nspname=$1 AND c.relname IN (
        'idx_external_pr_link_id', 'idx_external_pr_link_identity',
        'idx_external_pr_link_issue_state', 'idx_external_pr_receipt_idempotency',
        'external_pr_reconcile_work_identity_uidx',
        'external_pr_reconcile_work_claim_idx',
        'external_pr_reconcile_work_issue_idx',
        'external_pr_reconcile_finalization_work_uidx',
        'comment_external_pr_finalization_key_uidx',
        'inbox_item_delivery_key_uidx',
        'external_pr_reconcile_work_pkey',
        'external_pr_reconcile_finalization_pkey'
    )
    UNION ALL
    SELECT 'trigger', t.tgname, pg_get_triggerdef(t.oid)
    FROM pg_trigger t
    JOIN pg_class c ON c.oid=t.tgrelid
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname=$1 AND NOT t.tgisinternal AND t.tgname IN (
        'issue_completion_lock_on_status', 'external_pr_link_completion_lock',
        'issue_pull_request_completion_lock', 'github_pull_request_completion_lock',
        'issue_vcs_pull_request_completion_lock', 'vcs_pull_request_completion_lock'
    )
) catalog
ORDER BY kind, name`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func legacyExternalPRSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var value string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(string_agg(
provider || '|' || external_repo || '|' || external_number::text || '|' || state,
',' ORDER BY provider, external_repo, external_number), '') FROM external_pull_request_link`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertContextContinuitySchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, scenario string, wantLegacyRow bool) {
	t.Helper()
	expectedIndexDefinition := map[string][]string{
		"idx_external_pr_link_id":                      {"external_pull_request_link USING btree (id)"},
		"idx_external_pr_link_identity":                {"external_pull_request_link USING btree (workspace_id, provider, external_repo, external_number)"},
		"idx_external_pr_link_issue_state":             {"external_pull_request_link USING btree (workspace_id, issue_id, state)", "open", "draft", "link_confidence"},
		"idx_external_pr_receipt_idempotency":          {"external_pull_request_receipt USING btree (workspace_id, idempotency_key)"},
		"external_pr_reconcile_work_identity_uidx":     {"external_pr_reconcile_work USING btree (workspace_id, kind, link_id, source_revision)"},
		"external_pr_reconcile_work_claim_idx":         {"external_pr_reconcile_work USING btree (state, next_attempt_at, lease_expires_at, updated_at)"},
		"external_pr_reconcile_work_issue_idx":         {"external_pr_reconcile_work USING btree (workspace_id, issue_id, state, updated_at)"},
		"external_pr_reconcile_finalization_work_uidx": {"external_pr_reconcile_finalization USING btree (work_id)"},
		"comment_external_pr_finalization_key_uidx":    {"comment USING btree (finalization_key)"},
		"inbox_item_delivery_key_uidx":                 {"inbox_item USING btree (workspace_id, recipient_type, recipient_id, delivery_key)"},
		"external_pr_reconcile_work_pkey":              {"external_pr_reconcile_work USING btree (id)"},
		"external_pr_reconcile_finalization_pkey":      {"external_pr_reconcile_finalization USING btree (id)"},
	}
	// T016 retires workspace_workload_authority*; External PR indexes remain.
	for _, index := range []string{
		"idx_external_pr_link_id",
		"idx_external_pr_link_identity",
		"idx_external_pr_link_issue_state",
		"idx_external_pr_receipt_idempotency",
		"external_pr_reconcile_work_identity_uidx",
		"external_pr_reconcile_work_claim_idx",
		"external_pr_reconcile_work_issue_idx",
		"external_pr_reconcile_finalization_work_uidx",
		"comment_external_pr_finalization_key_uidx",
		"inbox_item_delivery_key_uidx",
		"external_pr_reconcile_work_pkey",
		"external_pr_reconcile_finalization_pkey",
	} {
		var ready, valid bool
		var columns, predicate string
		if err := pool.QueryRow(ctx, `
SELECT i.indisready, i.indisvalid,
       pg_get_indexdef(i.indexrelid), COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
FROM pg_index i
JOIN pg_class c ON c.oid=i.indexrelid
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname=$2`, schema, index).Scan(&ready, &valid, &columns, &predicate); err != nil {
			t.Fatalf("load index %s: %v", index, err)
		}
		if !ready || !valid || columns == "" {
			t.Fatalf("index %s ready=%v valid=%v definition=%q predicate=%q", index, ready, valid, columns, predicate)
		}
		if index == "external_pr_reconcile_finalization_work_uidx" && predicate != "(work_id IS NOT NULL)" {
			t.Fatalf("index %s predicate=%q, want non-null work predicate", index, predicate)
		}
		if index == "comment_external_pr_finalization_key_uidx" && predicate != "(finalization_key IS NOT NULL)" {
			t.Fatalf("index %s predicate=%q, want non-null finalization predicate", index, predicate)
		}
		if index == "inbox_item_delivery_key_uidx" && predicate != "(delivery_key IS NOT NULL)" {
			t.Fatalf("index %s predicate=%q, want non-null delivery-key predicate", index, predicate)
		}
		definition := columns + " " + predicate
		for _, fragment := range expectedIndexDefinition[index] {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("index %s definition=%q predicate=%q missing %q", index, columns, predicate, fragment)
			}
		}
	}
	var legacyGlobalIndex int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_class c
JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname=$1 AND c.relname='idx_external_pr_link_idempotency'`, schema).Scan(&legacyGlobalIndex); err != nil {
		t.Fatalf("check legacy global idempotency index: %v", err)
	}
	if legacyGlobalIndex != 0 {
		t.Fatalf("legacy global idempotency index still exists in %s", scenario)
	}
	if scenario == "historical_135" {
		rows, err := pool.Query(ctx, `
SELECT c.contype::text,
       string_agg(att.attname, ',' ORDER BY cols.ordinality),
       i.indisready, i.indisvalid,
       COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
FROM pg_constraint c
JOIN pg_class rel ON rel.oid=c.conrelid
JOIN pg_namespace ns ON ns.oid=rel.relnamespace
JOIN pg_index i ON i.indexrelid=c.conindid
JOIN LATERAL unnest(c.conkey) WITH ORDINALITY cols(attnum, ordinality) ON TRUE
JOIN pg_attribute att ON att.attrelid=rel.oid AND att.attnum=cols.attnum
WHERE ns.nspname=$1 AND rel.relname='external_pull_request_link' AND c.contype IN ('p','u')
GROUP BY c.contype, c.conname, i.indisready, i.indisvalid, i.indpred, i.indrelid`, schema)
		if err != nil {
			t.Fatalf("load historical constraint indexes: %v", err)
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var constraintType, columns, predicate string
			var ready, valid bool
			if err := rows.Scan(&constraintType, &columns, &ready, &valid, &predicate); err != nil {
				t.Fatal(err)
			}
			if !ready || !valid || predicate != "" {
				t.Fatalf("historical constraint index type=%s columns=%s ready=%v valid=%v predicate=%q", constraintType, columns, ready, valid, predicate)
			}
			seen[constraintType+":"+columns] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"p:id", "u:workspace_id,provider,external_repo,external_number"} {
			if !seen[want] {
				t.Fatalf("historical constraint index %q missing; got=%v", want, seen)
			}
		}
	}

	var foreignKeys int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_constraint c
JOIN pg_class rel ON rel.oid=c.conrelid
JOIN pg_namespace ns ON ns.oid=rel.relnamespace
WHERE ns.nspname=$1 AND rel.relname='external_pull_request_link' AND c.contype='f'`, schema).Scan(&foreignKeys); err != nil {
		t.Fatalf("count external PR foreign keys: %v", err)
	}
	if foreignKeys != 0 {
		t.Fatalf("external PR table retained %d foreign keys", foreignKeys)
	}
	// T016: dead authority/delegation tables must be gone after 292-299.
	for _, table := range []string{
		"workspace_workload_authority",
		"workload_pr_merge_delegation",
		"workload_pr_merge_delegation_event",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM pg_class c
  JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r'
)`, schema, table).Scan(&exists); err != nil {
			t.Fatalf("check retired table %s: %v", table, err)
		}
		if exists {
			t.Fatalf("retired table %s still exists after T016 migrations", table)
		}
	}
	var legacyRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM external_pull_request_link
WHERE provider='ags' AND external_repo='jackie/agent-kit' AND external_number=49`).Scan(&legacyRows); err != nil {
		t.Fatalf("count legacy external PR rows: %v", err)
	}
	want := 0
	if wantLegacyRow {
		want = 1
	}
	if legacyRows != want {
		t.Fatalf("legacy external PR rows=%d want=%d", legacyRows, want)
	}
	// T017: projection columns and link.idempotency_key must be gone.
	for _, col := range []string{
		"target_instance", "canonical_repository_id", "canonical_repository",
		"provider_binding_id", "provider_binding_revision", "provider_repository",
		"expected_head_sha", "expected_base_sha", "base_ref",
		"delegated_merge_method", "projection_facts_revision", "idempotency_key",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM information_schema.columns
  WHERE table_schema=$1 AND table_name='external_pull_request_link' AND column_name=$2
)`, schema, col).Scan(&exists); err != nil {
			t.Fatalf("check dropped column %s: %v", col, err)
		}
		if exists {
			t.Fatalf("T017 column %s still present on external_pull_request_link", col)
		}
	}
}
