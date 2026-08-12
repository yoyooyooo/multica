package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/attributionbackfill"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/migrations"
	"github.com/multica-ai/multica/server/internal/taskusagebackfill"
)

// preMigrationHook runs work that must happen before a specific
// migration is applied during `migrate up`. Hooks are idempotent and
// must not depend on the migration loop's session-pinned advisory lock
// — they run on the pool, not on the loop's pinned conn, so they can
// safely acquire other session-level locks (e.g. advisory lock 4246
// for the task_usage hourly rollup).
//
// Returning an error aborts the migration run. The corresponding
// migration is NOT recorded in schema_migrations, so the next run will
// retry the hook + migration.
type preMigrationHook func(ctx context.Context, pool *pgxpool.Pool) error

// preMigrationHooks wires migration version → hook. The version key is
// the file basename without the `.up.sql` suffix, matching what
// `migrations.ExtractVersion` returns.
//
// MUL-2957: the v0.3.4 → current direct-upgrade path needs the hourly
// rollup seeded BEFORE migration 103 evaluates its fail-closed lag
// guard, because at `cmd/migrate up` time the server has not yet
// started so neither the legacy pg_cron job nor the new app scheduler
// can advance the watermark. The hook runs the same idempotent
// monthly-slice backfill that
// `cmd/backfill_task_usage_hourly` exposes to operators.
//
// MUL-4897 / GH #5544: migration 198 VALIDATEs the strict attribution
// constraint installed by 197, which drops migration 190's
// originator_source IS NULL exemption. Self-hosted databases never ran the
// out-of-band backfill that Multica's cloud did, so their legacy rows make
// 198 fail closed and the backend refuses to start. The hook reconciles
// those rows (accountable_user_id := originator_user_id) idempotently BEFORE
// VALIDATE, so a stuck-at-197 instance auto-heals on `migrate up` with no
// manual SQL. A higher-numbered migration cannot help — the instance never
// reaches a version above the failing 198.
//
// GH #6388: migration 257 builds a replacement unique index concurrently. A
// failed build can leave an INVALID relation that IF NOT EXISTS would otherwise
// mistake for a successful retry. The hook removes only that invalid leftover;
// migration 257 can then rebuild it while the valid v1 index remains in place.
//
// MUL-5823: migration 261 replaces the terminal-task partial index the same
// way, so it carries the same hazard — an INVALID v2 leftover recorded as
// success would let migration 262 drop the still-valid v1, leaving all four
// dashboard rollups on a full table scan.
var preMigrationHooks = map[string]preMigrationHook{
	"103_drop_legacy_daily_rollups":                         runTaskUsageHourlyHook,
	"198_agent_task_attribution_strict_constraint_validate": runAttributionStrictHook,
	"257_agent_task_queue_channel_media_pending_unique_v2":  cleanupInvalidConcurrentIndexHook("idx_one_pending_task_per_issue_agent_v2"),
	"261_agent_task_queue_terminal_completed_at_v2":         cleanupInvalidConcurrentIndexHook("idx_agent_task_queue_terminal_completed_at_v2"),
	// These non-authority cleanup indexes use ordinary pre-hooks rather than
	// historical reconciliation: if CREATE INDEX CONCURRENTLY leaves an invalid
	// artifact before ledger commit, the next run removes/rebuilds it; once
	// ledgered, later migrations remain free to evolve the definition.
	// Historical live ledger names (pre-formal renumber) keep recovery hooks so
	// dual-ledger mini dumps can still self-heal invalid concurrent artifacts.
	"276_external_pr_link_issue_updated_index":               reconcileMigrationIndex(externalPRCleanupIndexSpecs[0]),
	"277_external_pr_receipt_issue_cleanup_index":            reconcileMigrationIndex(externalPRCleanupIndexSpecs[1]),
	"279_workload_pr_merge_delegation_id_index":              reconcileMigrationIndex(prMergeDelegationIndexSpecs[0]),
	"280_workload_pr_merge_delegation_active_index":          reconcileMigrationIndex(prMergeDelegationIndexSpecs[1]),
	"281_workload_pr_merge_delegation_consumer_intent_index": reconcileMigrationIndex(prMergeDelegationIndexSpecs[2]),
	"282_workload_pr_merge_delegation_issue_state_index":     reconcileMigrationIndex(prMergeDelegationIndexSpecs[3]),
	"283_workload_pr_merge_delegation_event_id_index":        reconcileMigrationIndex(prMergeDelegationIndexSpecs[4]),
	"284_workload_pr_merge_delegation_event_history_index":   reconcileMigrationIndex(prMergeDelegationIndexSpecs[5]),
	// Formal fork/v0.4.22+ renumbered filenames.
	"283_external_pr_link_issue_updated_index":               reconcileMigrationIndex(externalPRCleanupIndexSpecs[0]),
	"284_external_pr_receipt_issue_cleanup_index":            reconcileMigrationIndex(externalPRCleanupIndexSpecs[1]),
	"286_workload_pr_merge_delegation_id_index":              reconcileMigrationIndex(prMergeDelegationIndexSpecs[0]),
	"287_workload_pr_merge_delegation_active_index":          reconcileMigrationIndex(prMergeDelegationIndexSpecs[1]),
	"288_workload_pr_merge_delegation_consumer_intent_index": reconcileMigrationIndex(prMergeDelegationIndexSpecs[2]),
	"289_workload_pr_merge_delegation_issue_state_index":     reconcileMigrationIndex(prMergeDelegationIndexSpecs[3]),
	"290_workload_pr_merge_delegation_event_id_index":        reconcileMigrationIndex(prMergeDelegationIndexSpecs[4]),
	"291_workload_pr_merge_delegation_event_history_index":   reconcileMigrationIndex(prMergeDelegationIndexSpecs[5]),
}

// cleanupInvalidConcurrentIndexHook removes an INVALID index left by an
// interrupted or failed CREATE INDEX CONCURRENTLY before the migration retries.
// Without this guard, CREATE INDEX ... IF NOT EXISTS would treat the leftover
// relation as success and allow a later migration to drop the still-valid old
// index. Non-index relations fail closed instead of being dropped implicitly.
func cleanupInvalidConcurrentIndexHook(indexRegclass string) preMigrationHook {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		var schemaName, relationName string
		var isIndex, isValid bool
		err := pool.QueryRow(ctx, `
			SELECT n.nspname, c.relname, c.relkind = 'i', COALESCE(i.indisvalid, FALSE)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_index i ON i.indexrelid = c.oid
			WHERE c.oid = to_regclass($1)
		`, indexRegclass).Scan(&schemaName, &relationName, &isIndex, &isValid)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect concurrent index %q: %w", indexRegclass, err)
		}
		if !isIndex {
			return fmt.Errorf("relation %q exists but is not an index", indexRegclass)
		}
		if isValid {
			return nil
		}

		qualifiedName := pgx.Identifier{schemaName, relationName}.Sanitize()
		if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualifiedName); err != nil {
			return fmt.Errorf("drop invalid concurrent index %s: %w", qualifiedName, err)
		}
		slog.Warn("removed invalid index before migration retry", "index", qualifiedName)
		return nil
	}
}

// Formal fence filename after fork/v0.4.22 renumber. Historical dual-ledger
// dumps may still carry 275_*; both are accepted as the same fence epoch.
const externalPRIndexReconciliationFenceVersion = "282_external_pr_index_reconciliation_fence"
const externalPRIndexReconciliationFenceVersionLegacy = "275_external_pr_index_reconciliation_fence"

// reconciliationMigrationHooks run before the ledger skip check only until
// the External PR index fence records the final catalog. Concurrent index
// creation can leave an invalid same-name catalog entry, and older runners
// using IF NOT EXISTS may already have ledgered that invalid artifact.
var reconciliationMigrationHooks = map[string]preMigrationHook{
	// Historical live ledger names.
	"266_external_pr_link_id_unique_index":                reconcileMigrationIndex(externalPRIndexSpecs[0]),
	"267_external_pr_link_identity_index":                 reconcileMigrationIndex(externalPRIndexSpecs[1]),
	"268_external_pr_link_issue_state_index":              reconcileMigrationIndex(externalPRIndexSpecs[2]),
	"269_external_pr_link_idempotency_index":              reconcileMigrationIndex(externalPRIndexSpecs[3]),
	"271_workspace_workload_authority_workspace_id_index": reconcileMigrationIndex(externalPRIndexSpecs[4]),
	"273_external_pr_link_workspace_idempotency_index":    reconcileMigrationIndex(externalPRIndexSpecs[5]),
	"274_external_pr_legacy_idempotency_index_remove":     verifyExternalPRIndexAuthorities,
	externalPRIndexReconciliationFenceVersionLegacy:       reconcileAndVerifyExternalPRIndexAuthorities,
	// Formal fork/v0.4.22+ renumbered filenames.
	"273_external_pr_link_id_unique_index":                reconcileMigrationIndex(externalPRIndexSpecs[0]),
	"274_external_pr_link_identity_index":                 reconcileMigrationIndex(externalPRIndexSpecs[1]),
	"275_external_pr_link_issue_state_index":              reconcileMigrationIndex(externalPRIndexSpecs[2]),
	"276_external_pr_link_idempotency_index":              reconcileMigrationIndex(externalPRIndexSpecs[3]),
	"278_workspace_workload_authority_workspace_id_index": reconcileMigrationIndex(externalPRIndexSpecs[4]),
	"280_external_pr_link_workspace_idempotency_index":    reconcileMigrationIndex(externalPRIndexSpecs[5]),
	"281_external_pr_legacy_idempotency_index_remove":     verifyExternalPRIndexAuthorities,
	externalPRIndexReconciliationFenceVersion:             reconcileAndVerifyExternalPRIndexAuthorities,
}

func runTaskUsageHourlyHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := taskusagebackfill.Hook(ctx, pool, taskusagebackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("task_usage_hourly pre-103 hook: %w", err)
	}
	if res.Skipped != "" {
		slog.Info("task_usage hourly rollup hook: skipped",
			"reason", res.Skipped,
			"watermark_stamped", res.WatermarkStamped)
		return nil
	}
	slog.Info("task_usage hourly rollup hook: backfill complete",
		"slices", res.SlicesProcessed,
		"rows_touched", res.RowsTouched,
		"from", res.From.Format("2006-01-02T15:04:05Z07:00"),
		"to", res.To.Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

// runAttributionStrictHook backfills accountable_user_id from
// originator_user_id before migration 198 validates the strict attribution
// constraint, so self-hosted upgrades that never ran the out-of-band
// backfill recover automatically (GH #5544 / MUL-4897).
func runAttributionStrictHook(ctx context.Context, pool *pgxpool.Pool) error {
	res, err := attributionbackfill.Hook(ctx, pool, attributionbackfill.HookOptions{})
	if err != nil {
		return fmt.Errorf("attribution strict-constraint pre-198 hook: %w", err)
	}
	slog.Info("attribution backfill hook: complete",
		"rows_backfilled", res.RowsBackfilled,
		"batches", res.Batches,
		"mismatch_normalized", res.MismatchNormalized)
	return nil
}

type migrationIndexSpec struct {
	Name                string
	Table               string
	Unique              bool
	Columns             []string
	PredicateNormalized string
	PredicateSQL        string
}

var externalPRCleanupIndexSpecs = []migrationIndexSpec{
	{Name: "idx_external_pr_link_workspace_issue_updated", Table: "external_pull_request_link", Columns: []string{"workspace_id", "issue_id", "updated_at"}},
	{Name: "idx_external_pr_receipt_workspace_issue", Table: "external_pull_request_receipt", Columns: []string{"workspace_id", "issue_id"}},
}

var prMergeDelegationIndexSpecs = []migrationIndexSpec{
	{Name: "workload_pr_merge_delegation_id_uidx", Table: "workload_pr_merge_delegation", Unique: true, Columns: []string{"id"}},
	{Name: "workload_pr_merge_delegation_current_execution_uidx", Table: "workload_pr_merge_delegation", Unique: true, Columns: []string{"workspace_id", "task_id", "execution_id", "operation"}, PredicateNormalized: "state=anyarray[pending_approval,approved]", PredicateSQL: "state IN ('pending_approval', 'approved')"},
	{Name: "workload_pr_merge_delegation_consumer_intent_uidx", Table: "workload_pr_merge_delegation", Unique: true, Columns: []string{"consumer_instance_id", "consumer_intent_id"}, PredicateNormalized: "consumer_intent_idisnotnull", PredicateSQL: "consumer_intent_id IS NOT NULL"},
	{Name: "workload_pr_merge_delegation_issue_state_idx", Table: "workload_pr_merge_delegation", Columns: []string{"workspace_id", "issue_id", "state", "updated_at"}},
	{Name: "workload_pr_merge_delegation_event_id_uidx", Table: "workload_pr_merge_delegation_event", Unique: true, Columns: []string{"id"}},
	{Name: "workload_pr_merge_delegation_event_history_idx", Table: "workload_pr_merge_delegation_event", Columns: []string{"delegation_id", "created_at", "id"}},
}

var externalPRIndexSpecs = []migrationIndexSpec{
	{Name: "idx_external_pr_link_id", Table: "external_pull_request_link", Unique: true, Columns: []string{"id"}},
	{Name: "idx_external_pr_link_identity", Table: "external_pull_request_link", Unique: true, Columns: []string{"workspace_id", "provider", "external_repo", "external_number"}},
	{Name: "idx_external_pr_link_issue_state", Table: "external_pull_request_link", Columns: []string{"workspace_id", "issue_id", "state"}, PredicateNormalized: "state=anyarray[open,draft]andlink_confidence=authoritative"},
	{Name: "idx_external_pr_receipt_idempotency", Table: "external_pull_request_receipt", Unique: true, Columns: []string{"workspace_id", "idempotency_key"}},
	{Name: "workspace_workload_authority_workspace_id_uidx", Table: "workspace_workload_authority", Unique: true, Columns: []string{"workspace_id"}},
	{Name: "idx_external_pr_link_workspace_idempotency", Table: "external_pull_request_link", Unique: true, Columns: []string{"workspace_id", "idempotency_key"}, PredicateNormalized: "idempotency_keyisnotnull"},
}

func reconcileMigrationIndex(spec migrationIndexSpec) preMigrationHook {
	return func(ctx context.Context, pool *pgxpool.Pool) error {
		schema, exact, exists, err := inspectMigrationIndex(ctx, pool, spec)
		if err != nil {
			return err
		}
		if exact {
			return nil
		}
		if exists {
			indexName := pgx.Identifier{schema, spec.Name}.Sanitize()
			if _, err := pool.Exec(ctx, "DROP INDEX CONCURRENTLY "+indexName); err != nil {
				return fmt.Errorf("remove invalid or wrong-definition index %s: %w", spec.Name, err)
			}
		}
		if _, err := pool.Exec(ctx, createMigrationIndexSQL(schema, spec)); err != nil {
			return fmt.Errorf("rebuild index %s: %w", spec.Name, err)
		}
		_, exact, _, err = inspectMigrationIndex(ctx, pool, spec)
		if err != nil {
			return err
		}
		if !exact {
			return fmt.Errorf("index %s did not converge to its exact ready/valid definition", spec.Name)
		}
		return nil
	}
}

func verifyExternalPRIndexAuthorities(ctx context.Context, pool *pgxpool.Pool) error {
	for _, spec := range externalPRIndexSpecs {
		_, exact, _, err := inspectMigrationIndex(ctx, pool, spec)
		if err != nil {
			return err
		}
		if !exact {
			return fmt.Errorf("refuse legacy idempotency index removal: required index %s is not exact, ready, and valid", spec.Name)
		}
	}
	return nil
}

func reconcileAndVerifyExternalPRIndexAuthorities(ctx context.Context, pool *pgxpool.Pool) error {
	for _, spec := range externalPRIndexSpecs {
		if err := reconcileMigrationIndex(spec)(ctx, pool); err != nil {
			return err
		}
	}
	return verifyExternalPRIndexAuthorities(ctx, pool)
}

func inspectMigrationIndex(ctx context.Context, pool *pgxpool.Pool, spec migrationIndexSpec) (schema string, exact, exists bool, err error) {
	if err = pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return "", false, false, fmt.Errorf("resolve current schema: %w", err)
	}
	var table string
	var unique, ready, valid bool
	var keyAttributeCount, totalAttributeCount int16
	var columns []string
	var predicate string
	err = pool.QueryRow(ctx, `
SELECT tbl.relname, i.indisunique, i.indisready, i.indisvalid,
       i.indnkeyatts, i.indnatts,
       ARRAY(
         SELECT a.attname
         FROM unnest(i.indkey::smallint[]) WITH ORDINALITY AS key(attnum, position)
         JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.attnum
         ORDER BY key.position
       ),
       COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
FROM pg_class idx
JOIN pg_namespace n ON n.oid=idx.relnamespace
JOIN pg_index i ON i.indexrelid=idx.oid
JOIN pg_class tbl ON tbl.oid=i.indrelid
WHERE n.nspname=$1 AND idx.relname=$2`, schema, spec.Name).Scan(&table, &unique, &ready, &valid, &keyAttributeCount, &totalAttributeCount, &columns, &predicate)
	if err != nil {
		if err == pgx.ErrNoRows {
			var relationExists bool
			if scanErr := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, schema+"."+spec.Name).Scan(&relationExists); scanErr != nil {
				return schema, false, false, fmt.Errorf("inspect index relation %s: %w", spec.Name, scanErr)
			}
			if relationExists {
				return schema, false, true, fmt.Errorf("relation %s exists but is not an index", spec.Name)
			}
			return schema, false, false, nil
		}
		return schema, false, false, fmt.Errorf("inspect index %s: %w", spec.Name, err)
	}
	normalizedPredicate := strings.ToLower(strings.NewReplacer(" ", "", "\n", "", "\t", "", "(", "", ")", "", "::text", "", "'", "").Replace(predicate))
	predicateExact := normalizedPredicate == spec.PredicateNormalized
	exact = table == spec.Table && unique == spec.Unique && ready && valid &&
		int(keyAttributeCount) == len(spec.Columns) && totalAttributeCount == keyAttributeCount &&
		equalMigrationIndexColumns(columns, spec.Columns) && predicateExact
	return schema, exact, true, nil
}

func createMigrationIndexSQL(schema string, spec migrationIndexSpec) string {
	verb := "CREATE INDEX CONCURRENTLY "
	if spec.Unique {
		verb = "CREATE UNIQUE INDEX CONCURRENTLY "
	}
	columns := make([]string, len(spec.Columns))
	for i, column := range spec.Columns {
		columns[i] = pgx.Identifier{column}.Sanitize()
	}
	// PostgreSQL places the index in the table's schema and does not accept a
	// schema-qualified index name in CREATE INDEX. The table remains qualified.
	sql := verb + pgx.Identifier{spec.Name}.Sanitize() + " ON " + pgx.Identifier{schema, spec.Table}.Sanitize() + "(" + strings.Join(columns, ", ") + ")"
	if spec.PredicateSQL != "" {
		sql += " WHERE " + spec.PredicateSQL
	} else if spec.Name == "idx_external_pr_link_issue_state" {
		sql += " WHERE state IN ('open', 'draft') AND link_confidence = 'authoritative'"
	} else if spec.Name == "idx_external_pr_link_workspace_idempotency" {
		sql += " WHERE idempotency_key IS NOT NULL"
	}
	return sql
}

func equalMigrationIndexColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// migrationAdvisoryLockKey is the int64 identifier used with Postgres
// pg_advisory_lock to serialize the migration loop across concurrent
// runners (multi-replica backend Deployment, scale-up, or a manual
// `migrate up` overlapping with pod startup). The exact value is
// arbitrary — it just needs to be stable across every process that runs
// migrations against the same database. See GitHub multica-ai/multica#3647.
const migrationAdvisoryLockKey int64 = 7244554146635925501

// defaultSchemaMigrationsTable is the unqualified name of the bookkeeping
// table that tracks which migrations have been applied. Tests override
// this so a concurrent-race harness can run against the same shared
// Postgres without colliding with the production table.
const defaultSchemaMigrationsTable = "schema_migrations"

// runOptions carries everything runMigrations needs that is not the
// pool itself. Tests use it to inject a hermetic migrations directory,
// a unique per-test bookkeeping table, and a unique advisory-lock key
// that doesn't collide with any other migration runner sharing the same
// Postgres instance.
type runOptions struct {
	// Direction is "up" or "down".
	Direction string
	// Files is the ordered list of .sql files to apply. Production callers
	// pass migrations.Files(direction); tests pass a curated set written
	// to a t.TempDir().
	Files []string
	// SchemaMigrationsTable is the bookkeeping table to read/write.
	// May be schema-qualified (e.g. "migrate_test_xyz.schema_migrations").
	// Empty means defaultSchemaMigrationsTable.
	SchemaMigrationsTable string
	// AdvisoryLockKey is the int64 used with pg_advisory_lock. Zero means
	// migrationAdvisoryLockKey. Tests pass a unique key per run so
	// concurrent test workers do not block on the production migration
	// runner if it happens to share the database.
	AdvisoryLockKey int64
	// Hooks run only for migrations that are not yet ledgered.
	Hooks map[string]preMigrationHook
	// ReconcileHooks run before the ledger check, including for already-applied
	// migrations. They are reserved for retry-safe catalog authorities whose
	// validity must be re-established after a failed concurrent build.
	ReconcileHooks map[string]preMigrationHook
	// ReconcileFenceVersion disables the historical reconciliation hooks once
	// its ledger row exists, allowing later migrations to evolve those indexes.
	ReconcileFenceVersion string
}

func main() {
	logger.Init()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	direction := os.Args[1]
	if direction != "up" && direction != "down" {
		fmt.Println("Usage: go run ./cmd/migrate <up|down>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}

	files, err := migrations.Files(direction)
	if err != nil {
		slog.Error("failed to find migration files", "error", err)
		os.Exit(1)
	}

	if err := runMigrations(ctx, pool, runOptions{
		Direction:             direction,
		Files:                 files,
		Hooks:                 preMigrationHooks,
		ReconcileHooks:        reconciliationMigrationHooks,
		ReconcileFenceVersion: externalPRIndexReconciliationFenceVersion,
	}); err != nil {
		slog.Error("migration run failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("Done.")
}

// runMigrations applies (direction="up") or rolls back (direction="down")
// the given file list against the supplied pool, serialized through a
// Postgres session-level advisory lock so multiple concurrent runners
// (multi-replica startup, scale-up, manual migrate overlap) take turns
// instead of racing each other.
//
// It is safe to invoke concurrently from multiple goroutines or
// processes against the same database with the same options: every
// caller blocks on pg_advisory_lock, and once it is their turn the
// already-applied EXISTS check turns each finished migration into a
// no-op skip. See GitHub multica-ai/multica#3647 / MUL-2923.
func runMigrations(ctx context.Context, pool *pgxpool.Pool, opts runOptions) error {
	switch opts.Direction {
	case "up", "down":
		// ok
	default:
		return fmt.Errorf("invalid direction %q (want \"up\" or \"down\")", opts.Direction)
	}

	table := opts.SchemaMigrationsTable
	if table == "" {
		table = defaultSchemaMigrationsTable
	}
	tableIdent, err := quoteQualifiedIdentifier(table)
	if err != nil {
		return fmt.Errorf("invalid schema migrations table %q: %w", table, err)
	}
	lockKey := opts.AdvisoryLockKey
	if lockKey == 0 {
		lockKey = migrationAdvisoryLockKey
	}

	// pg_advisory_lock is scoped to a single session, so we must pin one
	// *pgxpool.Conn for the whole run — calling pool.Exec would attach the
	// lock to a random connection that pgxpool could hand back out before
	// the loop finishes, making the lock effectively a no-op. We use the
	// blocking pg_advisory_lock (not pg_try_*) so a late-arriving runner
	// queues behind the current one instead of crash-looping; once it
	// acquires the lock the EXISTS checks below turn finished migrations
	// into no-op skips.
	//
	// We deliberately do NOT wrap the loop in a single transaction: the
	// repo already ships migrations using CREATE INDEX CONCURRENTLY,
	// which Postgres rejects inside a transaction block.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	// Best-effort explicit unlock on the success path. On error returns
	// the defer still runs; on os.Exit error paths in main() it does not,
	// but session-level advisory locks are released automatically when
	// the connection closes at process exit, so the next runner is never
	// permanently blocked.
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
			slog.Warn("failed to release migration advisory lock", "error", err)
		}
	}()

	// Create migrations tracking table.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`, tableIdent)); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	existsSQL := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE version = $1)", tableIdent)
	insertSQL := fmt.Sprintf("INSERT INTO %s (version) VALUES ($1)", tableIdent)
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE version = $1", tableIdent)
	var reconciliationFenced bool
	if opts.Direction == "up" && opts.ReconcileFenceVersion != "" {
		if err := conn.QueryRow(ctx, existsSQL, opts.ReconcileFenceVersion).Scan(&reconciliationFenced); err != nil {
			return fmt.Errorf("check reconciliation fence %q: %w", opts.ReconcileFenceVersion, err)
		}
		// Accept the pre-renumber fence name on dual-ledger / historical dumps.
		if !reconciliationFenced && opts.ReconcileFenceVersion == externalPRIndexReconciliationFenceVersion {
			if err := conn.QueryRow(ctx, existsSQL, externalPRIndexReconciliationFenceVersionLegacy).Scan(&reconciliationFenced); err != nil {
				return fmt.Errorf("check legacy reconciliation fence %q: %w", externalPRIndexReconciliationFenceVersionLegacy, err)
			}
		}
	}

	for _, file := range opts.Files {
		version := migrations.ExtractVersion(file)

		if opts.Direction == "up" && !reconciliationFenced {
			if hook, ok := opts.ReconcileHooks[version]; ok && hook != nil {
				slog.Info("running migration reconciliation hook", "version", version)
				if err := hook(ctx, pool); err != nil {
					return fmt.Errorf("migration reconciliation hook for %q: %w", version, err)
				}
			}
		}

		var exists bool
		if err := conn.QueryRow(ctx, existsSQL, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %q: %w", version, err)
		}

		if opts.Direction == "up" {
			if exists {
				fmt.Printf("  skip  %s (already applied)\n", version)
				continue
			}
		} else {
			if !exists {
				fmt.Printf("  skip  %s (not applied)\n", version)
				continue
			}
			if opts.ReconcileFenceVersion != "" && version == opts.ReconcileFenceVersion {
				return fmt.Errorf("refuse rollback across forward-only reconciliation fence %q", version)
			}
		}

		sql, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", file, err)
		}

		// Run any pre-migration hook before the SQL file. Hooks
		// receive the *pgxpool.Pool (not the loop's pinned conn), so
		// they can acquire other session-level locks without
		// colliding with migrationAdvisoryLockKey. Hook failures
		// abort the run before schema_migrations is updated, so the
		// same version retries cleanly on the next invocation.
		if opts.Direction == "up" {
			if hook, ok := opts.Hooks[version]; ok && hook != nil {
				slog.Info("running pre-migration hook", "version", version)
				if err := hook(ctx, pool); err != nil {
					return fmt.Errorf("pre-migration hook for %q: %w", version, err)
				}
			}
		}

		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %q: %w", file, err)
		}

		if opts.Direction == "up" {
			_, err = conn.Exec(ctx, insertSQL, version)
		} else {
			_, err = conn.Exec(ctx, deleteSQL, version)
		}
		if err != nil {
			return fmt.Errorf("record migration %q: %w", version, err)
		}
		if opts.Direction == "up" && opts.ReconcileFenceVersion != "" && version == opts.ReconcileFenceVersion {
			reconciliationFenced = true
		}

		fmt.Printf("  %s  %s\n", opts.Direction, version)
	}

	return nil
}

// quoteQualifiedIdentifier safely quotes either an unqualified table
// name ("foo") or a schema-qualified name ("schema.foo") for embedding
// into a SQL statement. Postgres does not let parametrized queries
// supply identifiers, so we have to interpolate, but pgx.Identifier
// does the right escaping (double-quotes, embedded-quote handling).
//
// The accepted shape is exactly one or two dot-separated components.
// Names containing more than one dot are rejected outright rather than
// silently sanitized into a "schema"."b.c" reference, which is valid
// SQL but almost certainly not what the caller meant.
func quoteQualifiedIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty identifier")
	}
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("identifier %q has more than one dot; only schema.table is supported", name)
	}
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("empty component in %q", name)
		}
	}
	return pgx.Identifier(parts).Sanitize(), nil
}
