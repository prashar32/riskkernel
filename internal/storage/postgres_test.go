package storage

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresConformance runs the shared Store conformance suite against a real
// Postgres, proving the Postgres backend is at parity with SQLite. It is skipped
// unless RISKKERNEL_TEST_DATABASE_URL points at a disposable Postgres database —
// CI provides one as a service; locally, point it at a scratch instance. The test
// resets the target's public schema first, so it must be a throwaway database.
func TestPostgresConformance(t *testing.T) {
	dsn := os.Getenv("RISKKERNEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RISKKERNEL_TEST_DATABASE_URL to a disposable Postgres to run the Postgres conformance suite")
	}
	resetPublicSchema(t, dsn)

	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	storeConformance(t, s)
}

// TestPostgresMigrateDowngradeProtection asserts Postgres refuses to start when the
// on-disk schema is newer than the binary understands (the same downgrade guard the
// SQLite backend enforces).
func TestPostgresMigrateDowngradeProtection(t *testing.T) {
	dsn := os.Getenv("RISKKERNEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set RISKKERNEL_TEST_DATABASE_URL to run the Postgres downgrade-protection test")
	}
	resetPublicSchema(t, dsn)

	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	// Pretend a newer binary has run by recording a future migration version.
	max, err := maxMigrationVersionFS(pgMigrationsFS, pgMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(context.Background(),
		`INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES ($1, true, now())`, max+1)
	if err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	_ = s.Close()

	if _, err := OpenPostgres(dsn); err == nil {
		t.Fatal("OpenPostgres should refuse a schema newer than the binary")
	}
}

// resetPublicSchema drops and recreates the public schema so each run starts on a
// clean database.
func resetPublicSchema(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open for reset: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}
