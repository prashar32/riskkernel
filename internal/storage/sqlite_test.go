package storage

import (
	"context"
	"encoding/json"
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

	raw, err := json.Marshal(totals)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"runId", "calls", "promptTokens", "completionTokens", "dollars"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("marshaled totals missing %q: %s", key, raw)
		}
	}
	if _, ok := payload["RunID"]; ok {
		t.Fatalf("marshaled totals uses Go field names: %s", raw)
	}
	if payload["runId"] != "run-3" || payload["calls"] != float64(2) || payload["promptTokens"] != float64(300) || payload["completionTokens"] != float64(130) {
		t.Fatalf("marshaled totals = %s", raw)
	}
	if d := payload["dollars"].(float64); d < 0.0359 || d > 0.0361 {
		t.Fatalf("marshaled dollars = %v, want ~0.036", d)
	}
}

func TestSummarizeLedger(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	day1 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)

	mkRun := func(id, name, team string, created time.Time) {
		if err := s.UpsertRun(ctx, RunRecord{
			ID: id, Name: name, Status: "running",
			Metadata:  map[string]string{"team": team},
			CreatedAt: created, UpdatedAt: created,
		}); err != nil {
			t.Fatalf("seed run %s: %v", id, err)
		}
	}
	mkRun("run-a", "alice", "marketing", day1)
	mkRun("run-b", "bob", "marketing", day1)
	mkRun("run-c", "carol", "eng", day2)

	for _, e := range []LedgerEntry{
		{RunID: "run-a", StepIndex: 1, Provider: "anthropic", Model: "claude-haiku", PromptTokens: 100, CompletionTokens: 50, Dollars: 0.01, Priced: true, CreatedAt: day1},
		{RunID: "run-b", StepIndex: 1, Provider: "anthropic", Model: "claude-haiku", PromptTokens: 200, CompletionTokens: 80, Dollars: 0.012, Priced: true, CreatedAt: day1},
		{RunID: "run-b", StepIndex: 2, Provider: "anthropic", Model: "claude-haiku", PromptTokens: 50, CompletionTokens: 20, Dollars: 0.008, Priced: true, CreatedAt: day1.Add(time.Second)},
		{RunID: "run-c", StepIndex: 1, Provider: "openai", Model: "gpt-5", PromptTokens: 300, CompletionTokens: 100, Dollars: 0.05, Priced: true, CreatedAt: day2},
	} {
		if err := s.AppendLedger(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	asMap := func(sum UsageSummary) map[string]UsageGroup {
		m := map[string]UsageGroup{}
		for _, g := range sum.Groups {
			m[g.Key] = g
		}
		return m
	}
	approx := func(got, want float64) bool { return got > want-1e-9 && got < want+1e-9 }

	// by metadata.team — spend rolled up across runs by a user-supplied tag.
	sum, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "metadata.team"})
	if err != nil {
		t.Fatalf("by team: %v", err)
	}
	m := asMap(sum)
	if g := m["marketing"]; g.Calls != 3 || g.PromptTokens != 350 || g.CompletionTokens != 150 || !approx(g.Dollars, 0.03) {
		t.Fatalf("by team marketing = %+v", g)
	}
	if g := m["eng"]; g.Calls != 1 || !approx(g.Dollars, 0.05) {
		t.Fatalf("by team eng = %+v", g)
	}
	if sum.Total.Calls != 4 || !approx(sum.Total.Dollars, 0.08) {
		t.Fatalf("by team total = %+v", sum.Total)
	}
	if len(sum.Groups) != 2 || sum.Groups[0].Key != "eng" { // ordered by $ desc
		t.Fatalf("group order = %+v", sum.Groups)
	}

	// by provider
	sum, _ = s.SummarizeLedger(ctx, SummarizeOptions{By: "provider"})
	m = asMap(sum)
	if g := m["anthropic"]; g.Calls != 3 || !approx(g.Dollars, 0.03) {
		t.Fatalf("by provider anthropic = %+v", g)
	}
	if g := m["openai"]; g.Calls != 1 || !approx(g.Dollars, 0.05) {
		t.Fatalf("by provider openai = %+v", g)
	}

	// by name → one group per run
	sum, _ = s.SummarizeLedger(ctx, SummarizeOptions{By: "name"})
	if len(asMap(sum)) != 3 {
		t.Fatalf("by name groups = %+v", sum.Groups)
	}

	// by day, bounded to before day2 → only day1's three calls
	until := day2
	sum, err = s.SummarizeLedger(ctx, SummarizeOptions{By: "day", Until: &until})
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	m = asMap(sum)
	if len(m) != 1 {
		t.Fatalf("day groups (bounded) = %+v", sum.Groups)
	}
	if g := m["2026-05-20"]; g.Calls != 3 || !approx(g.Dollars, 0.03) {
		t.Fatalf("day 2026-05-20 = %+v", g)
	}

	// guardrails: unsupported dimension and unsafe metadata key are rejected.
	if _, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "bogus"}); err == nil {
		t.Fatal("expected error for unsupported dimension")
	}
	if _, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "metadata.bad key!"}); err == nil {
		t.Fatal("expected error for invalid metadata key")
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

func TestCheckpoints(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustRun(t, s, "run-cp", now)

	if _, err := s.LatestCheckpoint(ctx, "run-cp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for no checkpoints, got %v", err)
	}

	c1 := CheckpointRecord{RunID: "run-cp", StepIndex: 1, UsagePromptTokens: 100, UsageLoops: 1, CreatedAt: now}
	c2 := CheckpointRecord{RunID: "run-cp", StepIndex: 2, UsagePromptTokens: 250, UsageLoops: 2,
		Name: "after-tool", Payload: map[string]any{"messages": []any{"a", "b"}}, CreatedAt: now.Add(time.Second)}
	if err := s.SaveCheckpoint(ctx, c1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(ctx, c2); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestCheckpoint(ctx, "run-cp")
	if err != nil {
		t.Fatal(err)
	}
	if latest.StepIndex != 2 || latest.Name != "after-tool" || latest.UsagePromptTokens != 250 {
		t.Fatalf("latest = %+v", latest)
	}
	if latest.Payload["messages"] == nil {
		t.Errorf("payload not round-tripped: %v", latest.Payload)
	}

	all, err := s.ListCheckpoints(ctx, "run-cp")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListCheckpoints = %v, %v", all, err)
	}
	if all[0].StepIndex != 1 {
		t.Errorf("checkpoints not in order: %+v", all)
	}
}

func TestListRunsByStatus(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, tc := range []struct{ id, status string }{
		{"r-run", "running"}, {"r-halt", "halted"}, {"r-run2", "running"},
	} {
		if err := s.UpsertRun(ctx, RunRecord{ID: tc.id, Status: tc.status, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	running, err := s.ListRunsByStatus(ctx, "running")
	if err != nil || len(running) != 2 {
		t.Fatalf("running runs = %v, %v; want 2", running, err)
	}
}

func TestApprovals(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mustRun(t, s, "run-ap", now)

	a := ApprovalRecord{
		ID: "ap-1", RunID: "run-ap", StepIndex: 3, Tool: "mcp://shell", SideEffect: "exec",
		Arguments: map[string]any{"cmd": "ls"}, Status: ApprovalPending, CreatedAt: now,
	}
	if err := s.CreateApproval(ctx, a); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	pending, err := s.ListApprovals(ctx, ApprovalPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListApprovals pending = %v, %v", pending, err)
	}
	if pending[0].Arguments["cmd"] != "ls" {
		t.Errorf("args not round-tripped: %+v", pending[0].Arguments)
	}

	// Resolve it.
	if err := s.ResolveApproval(ctx, "ap-1", ApprovalApproved, "ok", "alice", now.Add(time.Minute)); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	got, _ := s.GetApproval(ctx, "ap-1")
	if got.Status != ApprovalApproved || got.DecidedBy != "alice" || got.DecidedAt == nil {
		t.Fatalf("resolved approval = %+v", got)
	}

	// Pending list now empty; double-resolve fails.
	if pend, _ := s.ListApprovals(ctx, ApprovalPending); len(pend) != 0 {
		t.Errorf("expected no pending after resolve, got %d", len(pend))
	}
	if err := s.ResolveApproval(ctx, "ap-1", ApprovalDenied, "", "", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-resolve should return ErrNotFound, got %v", err)
	}
}

func TestFacts(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := s.GetFact(ctx, "ns", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.PutFact(ctx, Fact{Namespace: "ns", Key: "lang", Value: "go", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Upsert overwrites.
	if err := s.PutFact(ctx, Fact{Namespace: "ns", Key: "lang", Value: "go+python", RunID: "r1", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetFact(ctx, "ns", "lang")
	if err != nil || got.Value != "go+python" || got.RunID != "r1" {
		t.Fatalf("GetFact = %+v, %v", got, err)
	}
	_ = s.PutFact(ctx, Fact{Namespace: "ns", Key: "db", Value: "sqlite", UpdatedAt: now})
	_ = s.PutFact(ctx, Fact{Namespace: "other", Key: "x", Value: "y", UpdatedAt: now})
	facts, err := s.ListFacts(ctx, "ns")
	if err != nil || len(facts) != 2 {
		t.Fatalf("ListFacts(ns) = %v, %v; want 2", facts, err)
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
