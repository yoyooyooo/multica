package lineage

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreSurvivesRestartAndPersistsIndexRows(t *testing.T) {
	pool := lineageTestPool(t)
	defer pool.Close()

	scope := Scope{MulticaInstanceID: "lineage-test-instance", WorkspaceID: uuid.NewString()}
	receipt := validReceipt(scope)
	seal(t, &receipt)

	firstStore := NewPostgresStore(pool)
	firstService := NewService(scope, firstStore)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM lineage_redaction WHERE receipt_id = $1`, receipt.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM lineage_observation WHERE receipt_id = $1`, receipt.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM lineage_receipt_anchor WHERE receipt_id = $1`, receipt.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM lineage_receipt WHERE id = $1`, receipt.ID)
	})

	created, err := firstService.Ingest(context.Background(), receipt)
	if err != nil {
		t.Fatalf("first Postgres ingestion: %v", err)
	}
	if !created.Created {
		t.Fatal("first Postgres ingestion must create a receipt")
	}

	// A new service/store pair models process restart: persistence, rather than
	// in-memory cache, must recognize the same idempotency tuple.
	restarted := NewService(scope, NewPostgresStore(pool))
	replay, err := restarted.Ingest(context.Background(), receipt)
	if err != nil {
		t.Fatalf("ingestion after restart: %v", err)
	}
	if replay.Created || replay.Receipt.ID != receipt.ID {
		t.Fatalf("restart replay = %#v, want existing %q", replay, receipt.ID)
	}

	var anchors, observations int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM lineage_receipt_anchor WHERE receipt_id = $1`, receipt.ID).Scan(&anchors); err != nil {
		t.Fatalf("count anchors: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM lineage_observation WHERE receipt_id = $1`, receipt.ID).Scan(&observations); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if anchors != len(receipt.Anchors) || observations != len(receipt.Observations) {
		t.Fatalf("persisted index rows = anchors:%d observations:%d, want anchors:%d observations:%d", anchors, observations, len(receipt.Anchors), len(receipt.Observations))
	}
}

func lineageTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("lineage integration test requires DATABASE_URL: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("lineage integration database is unavailable: %v", err)
	}
	return pool
}
