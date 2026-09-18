package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/forkmigrations"
	"github.com/multica-ai/multica/server/internal/migrations"
)

const (
	forkSchemaMigrationsTable = "fork_schema_migrations"
	forkMigrationAdvisoryLock = migrationAdvisoryLockKey + 1
)

func productionMigrationOptions(direction string) ([]runOptions, error) {
	upstreamFiles, err := migrations.Files(direction)
	if err != nil {
		return nil, fmt.Errorf("resolve upstream migrations: %w", err)
	}
	forkFiles, err := forkmigrations.Files(direction)
	if err != nil {
		return nil, fmt.Errorf("resolve fork migrations: %w", err)
	}
	upstream := runOptions{
		Direction: direction, Files: upstreamFiles,
		Hooks: hooksForDirection(direction), Conditions: conditionsForDirection(direction),
	}
	fork := runOptions{
		Direction: direction, Files: forkFiles,
		SchemaMigrationsTable: forkSchemaMigrationsTable, AdvisoryLockKey: forkMigrationAdvisoryLock,
	}
	if direction == "down" {
		return []runOptions{fork, upstream}, nil
	}
	return []runOptions{upstream, fork}, nil
}

func runProductionMigrations(ctx context.Context, pool *pgxpool.Pool, options []runOptions) error {
	for _, option := range options {
		if err := runMigrations(ctx, pool, option); err != nil {
			return err
		}
	}
	return nil
}
