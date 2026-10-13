package pgstore

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/tern/v2/migrate"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationsDir is where migrationFiles keeps the migrations.
const migrationsDir = "migrations"

// versionTable records which migrations have run.
const versionTable = schema + ".schema_version"

// Migrate brings the store's schema up to the one this version of the package
// queries, running each migration not yet applied in order, under an advisory
// lock, so replicas starting together migrate once. Run it before the store
// serves, as a role that may create the schema.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	defer c.Release()
	return migrateConn(ctx, c.Conn())
}

func migrateConn(ctx context.Context, c *pgx.Conn) error {
	// The version table lives in the schema, so the schema comes first.
	if _, err := c.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("pgstore: migrate: create schema: %w", err)
	}
	m, err := migrate.NewMigrator(ctx, c, versionTable)
	if err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	dir, err := fs.Sub(migrationFiles, migrationsDir)
	if err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	if err := m.LoadMigrations(dir); err != nil {
		return fmt.Errorf("pgstore: migrate: load: %w", err)
	}
	if err := m.Migrate(ctx); err != nil {
		return fmt.Errorf("pgstore: migrate: %w", err)
	}
	return nil
}
