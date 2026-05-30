package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func openTemp(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrate_FreshDB(t *testing.T) {
	s := openTemp(t)
	v, err := goose.GetDBVersion(s.db)
	if err != nil {
		t.Fatal(err)
	}
	max, _ := maxMigrationVersion()
	if v != max || max < 1 {
		t.Fatalf("schema version = %d, want %d (>=1)", v, max)
	}
}

func TestRunRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	r := RunRecord{
		ID: "run-1", Name: "demo", Status: "running",
		BudgetTokens: 1000, BudgetDollars: 5, BudgetLoops: 10, BudgetSeconds: 60,
		UsagePromptTokens: 100, UsageCompletionTokens: 50, UsageDollars: 0.3, UsageLoops: 2,
		Metadata: map[string]string{"team": "core"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertRun(ctx, r); err != nil {
		t.Fatalf("UpsertRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Name != "demo" || got.BudgetTokens != 1000 || got.UsageCompletionTokens != 50 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Metadata["team"] != "core" {
		t.Errorf("metadata = %v", got.Metadata)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, now)
	}

	// Upsert updates in place.
	r.Status = "halted"
	r.HaltReason = "token_budget_exceeded"
	r.UsagePromptTokens = 999
	if err := s.UpsertRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRun(ctx, "run-1")
	if got.Status != "halted" || got.HaltReason != "token_budget_exceeded" || got.UsagePromptTokens != 999 {
		t.Fatalf("update mismatch: %+v", got)
	}

	runs, err := s.ListRuns(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %v, %v", runs, err)
	}
}

func TestGetRun_NotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.GetRun(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSteps(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustRun(t, s, "run-2", now)

	if err := s.UpsertStep(ctx, StepRecord{RunID: "run-2", Index: 1, Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	ended := now.Add(time.Second)
	if err := s.UpsertStep(ctx, StepRecord{
		RunID: "run-2", Index: 1, Status: "completed",
		PromptTokens: 10, CompletionTokens: 20, Dollars: 0.01, StartedAt: now, EndedAt: &ended,
	}); err != nil {
		t.Fatal(err)
	}
	steps, err := s.ListSteps(ctx, "run-2")
	if err != nil || len(steps) != 1 {
		t.Fatalf("ListSteps = %v, %v", steps, err)
	}
	if steps[0].Status != "completed" || steps[0].CompletionTokens != 20 || steps[0].EndedAt == nil {
		t.Fatalf("step = %+v", steps[0])
	}
}

func TestLedgerAndTotals(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustRun(t, s, "run-3", now)

	entries := []LedgerEntry{
		{RunID: "run-3", StepIndex: 1, Provider: "anthropic", Model: "claude-sonnet-4-5",
			PromptTokens: 100, CompletionTokens: 50, Dollars: 0.012, Priced: true, ResponseID: "a", CreatedAt: now},
		{RunID: "run-3", StepIndex: 2, Provider: "anthropic", Model: "claude-sonnet-4-5",
			PromptTokens: 200, CompletionTokens: 80, Dollars: 0.024, Priced: true, ResponseID: "b", CreatedAt: now.Add(time.Second)},
	}
	for _, e := range entries {
		if err := s.AppendLedger(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.LedgerForRun(ctx, "run-3")
	if err != nil || len(got) != 2 {
		t.Fatalf("LedgerForRun = %v, %v", got, err)
	}
	if got[0].ResponseID != "a" || !got[0].Priced {
		t.Errorf("ledger[0] = %+v", got[0])
	}

	totals, err := s.Totals(ctx, "run-3")
	if err != nil {
		t.Fatal(err)
	}
	if totals.Calls != 2 || totals.PromptTokens != 300 || totals.CompletionTokens != 130 {
		t.Fatalf("totals = %+v", totals)
	}
	if d := totals.Dollars; d < 0.0359 || d > 0.0361 {
		t.Errorf("totals.Dollars = %v, want ~0.036", d)
	}
}

func TestToolCall(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustRun(t, s, "run-4", now)
	err := s.AppendToolCall(ctx, ToolCallRecord{
		ID: "tc-1", RunID: "run-4", StepIndex: 1, Tool: "mcp://shell",
		SideEffect: "write", Arguments: map[string]any{"cmd": "ls"}, Status: "pending", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("AppendToolCall: %v", err)
	}
}

func TestForeignKeyEnforced(t *testing.T) {
	// A ledger entry for a non-existent run must be rejected (foreign_keys ON).
	s := openTemp(t)
	err := s.AppendLedger(context.Background(), LedgerEntry{
		RunID: "ghost", StepIndex: 1, Provider: "anthropic", Model: "x", CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected foreign-key violation for unknown run")
	}
}

func TestDowngradeProtection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fwd.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a future binary having written a newer schema version.
	max, _ := maxMigrationVersion()
	if _, err := s.db.Exec(
		`INSERT INTO goose_db_version (version_id, is_applied, tstamp) VALUES (?, 1, CURRENT_TIMESTAMP)`,
		max+5); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	_ = s.Close()

	_, err = OpenSQLite(path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("expected ErrSchemaTooNew, got %v", err)
	}
}

func mustRun(t *testing.T, s *SQLite, id string, now time.Time) {
	t.Helper()
	if err := s.UpsertRun(context.Background(), RunRecord{
		ID: id, Status: "running", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}
