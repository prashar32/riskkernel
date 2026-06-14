package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// storeConformance exercises the full Store contract against a freshly-migrated,
// empty backend. Both the SQLite and Postgres backends run it, so the two stay at
// behavioral parity — a query that works on SQLite but not Postgres (or vice
// versa) fails here.
func storeConformance(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	day1 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)

	// --- runs: insert, get, update (upsert), list, list-by-status ---
	r := RunRecord{
		ID: "run-alpha", Name: "alpha", Status: "running",
		BudgetTokens: 1000, BudgetDollars: 5, BudgetLoops: 10, BudgetSeconds: 60,
		UsagePromptTokens: 100, UsageCompletionTokens: 50, UsageDollars: 0.01, UsageLoops: 1,
		PolicyRef: "bundle-x", Metadata: map[string]string{"team": "alpha"},
		CreatedAt: day1, UpdatedAt: day1,
	}
	if err := s.UpsertRun(ctx, r); err != nil {
		t.Fatalf("UpsertRun: %v", err)
	}
	got, err := s.GetRun(ctx, "run-alpha")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Name != "alpha" || got.Status != "running" || got.BudgetTokens != 1000 ||
		got.BudgetDollars != 5 || got.UsageCompletionTokens != 50 || got.PolicyRef != "bundle-x" ||
		got.Metadata["team"] != "alpha" {
		t.Fatalf("GetRun roundtrip mismatch: %+v", got)
	}
	if !got.CreatedAt.Equal(day1) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, day1)
	}

	// Upsert again (same id) → update status + usage; created_at preserved by caller.
	r.Status = "halted"
	r.HaltReason = "token_budget_exceeded"
	r.UsageDollars = 0.02
	r.UpdatedAt = day2
	if err := s.UpsertRun(ctx, r); err != nil {
		t.Fatalf("UpsertRun update: %v", err)
	}
	got, _ = s.GetRun(ctx, "run-alpha")
	if got.Status != "halted" || got.HaltReason != "token_budget_exceeded" || got.UsageDollars != 0.02 {
		t.Fatalf("update not applied: %+v", got)
	}

	// A second run, different metadata, for grouping/list assertions.
	r2 := RunRecord{ID: "run-beta", Name: "beta", Status: "running",
		Metadata: map[string]string{"team": "beta"}, CreatedAt: day2, UpdatedAt: day2}
	if err := s.UpsertRun(ctx, r2); err != nil {
		t.Fatalf("UpsertRun beta: %v", err)
	}

	all, err := s.ListRuns(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListRuns = %d (%v), want 2", len(all), err)
	}
	if all[0].ID != "run-beta" { // newest (created_at desc) first
		t.Errorf("ListRuns order: first = %q, want run-beta", all[0].ID)
	}
	running, err := s.ListRunsByStatus(ctx, "running")
	if err != nil || len(running) != 1 || running[0].ID != "run-beta" {
		t.Fatalf("ListRunsByStatus(running) = %+v (%v)", running, err)
	}

	// --- steps: upsert, update (close), list ---
	st := StepRecord{RunID: "run-alpha", Index: 1, Status: "running",
		PromptTokens: 100, CompletionTokens: 50, Dollars: 0.01, StartedAt: day1}
	if err := s.UpsertStep(ctx, st); err != nil {
		t.Fatalf("UpsertStep: %v", err)
	}
	ended := day1.Add(time.Second)
	st.Status, st.EndedAt = "done", &ended
	if err := s.UpsertStep(ctx, st); err != nil {
		t.Fatalf("UpsertStep close: %v", err)
	}
	steps, err := s.ListSteps(ctx, "run-alpha")
	if err != nil || len(steps) != 1 || steps[0].Status != "done" || steps[0].EndedAt == nil {
		t.Fatalf("ListSteps = %+v (%v)", steps, err)
	}

	// --- ledger: append, list, totals, summarize ---
	mustLedger(t, s, LedgerEntry{RunID: "run-alpha", StepIndex: 1, Provider: "anthropic",
		Model: "claude", PromptTokens: 100, CompletionTokens: 50, Dollars: 0.01, Priced: true,
		ResponseID: "resp-1", CreatedAt: day1})
	mustLedger(t, s, LedgerEntry{RunID: "run-beta", StepIndex: 1, Provider: "openai",
		Model: "gpt-4o", PromptTokens: 200, CompletionTokens: 20, Dollars: 0.03, Priced: true,
		ResponseID: "resp-2", CreatedAt: day2})

	led, err := s.LedgerForRun(ctx, "run-alpha")
	if err != nil || len(led) != 1 || led[0].Provider != "anthropic" || !led[0].Priced || led[0].ResponseID != "resp-1" {
		t.Fatalf("LedgerForRun = %+v (%v)", led, err)
	}
	tot, err := s.Totals(ctx, "run-alpha")
	if err != nil || tot.Calls != 1 || tot.PromptTokens != 100 || tot.CompletionTokens != 50 || tot.Dollars != 0.01 {
		t.Fatalf("Totals = %+v (%v)", tot, err)
	}

	// Summaries across both runs/ledger rows.
	byProvider, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "provider"})
	if err != nil || len(byProvider.Groups) != 2 || byProvider.Total.Calls != 2 || byProvider.Total.Dollars != 0.04 {
		t.Fatalf("SummarizeLedger provider = %+v (%v)", byProvider, err)
	}
	// Highest spend first: openai (0.03) before anthropic (0.01).
	if byProvider.Groups[0].Key != "openai" {
		t.Errorf("provider order: first = %q, want openai", byProvider.Groups[0].Key)
	}
	byDay, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "day"})
	if err != nil || len(byDay.Groups) != 2 {
		t.Fatalf("SummarizeLedger day = %+v (%v)", byDay, err)
	}
	byName, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "name"})
	if err != nil || len(byName.Groups) != 2 {
		t.Fatalf("SummarizeLedger name = %+v (%v)", byName, err)
	}
	byTeam, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "metadata.team"})
	if err != nil || len(byTeam.Groups) != 2 {
		t.Fatalf("SummarizeLedger metadata.team = %+v (%v)", byTeam, err)
	}
	// metadata grouping must actually read the JSON column, not bucket as '(none)'.
	keys := map[string]bool{byTeam.Groups[0].Key: true, byTeam.Groups[1].Key: true}
	if !keys["alpha"] || !keys["beta"] {
		t.Fatalf("metadata.team groups = %v, want alpha+beta", keys)
	}
	// A time window excludes day1's row.
	windowed, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "provider", Since: &day2})
	if err != nil || windowed.Total.Calls != 1 || windowed.Groups[0].Key != "openai" {
		t.Fatalf("SummarizeLedger windowed = %+v (%v)", windowed, err)
	}
	if _, err := s.SummarizeLedger(ctx, SummarizeOptions{By: "metadata.bad key"}); err == nil {
		t.Error("SummarizeLedger should reject an invalid metadata key")
	}

	// --- tool calls ---
	if err := s.AppendToolCall(ctx, ToolCallRecord{ID: "tc-1", RunID: "run-alpha", StepIndex: 1,
		Tool: "mcp://shell", SideEffect: "write", Arguments: map[string]any{"cmd": "ls"},
		Status: "approved", CreatedAt: day1}); err != nil {
		t.Fatalf("AppendToolCall: %v", err)
	}
	tcs, err := s.ListToolCalls(ctx, "run-alpha")
	if err != nil || len(tcs) != 1 || tcs[0].Tool != "mcp://shell" || tcs[0].Arguments["cmd"] != "ls" {
		t.Fatalf("ListToolCalls = %+v (%v)", tcs, err)
	}

	// --- facts: put, get, update, list ---
	if err := s.PutFact(ctx, Fact{Namespace: "ns", Key: "k", Value: "v1", RunID: "run-alpha", UpdatedAt: day1}); err != nil {
		t.Fatalf("PutFact: %v", err)
	}
	if err := s.PutFact(ctx, Fact{Namespace: "ns", Key: "k", Value: "v2", UpdatedAt: day2}); err != nil {
		t.Fatalf("PutFact update: %v", err)
	}
	f, err := s.GetFact(ctx, "ns", "k")
	if err != nil || f.Value != "v2" {
		t.Fatalf("GetFact = %+v (%v), want value v2", f, err)
	}
	facts, err := s.ListFacts(ctx, "ns")
	if err != nil || len(facts) != 1 {
		t.Fatalf("ListFacts = %d (%v), want 1", len(facts), err)
	}

	// --- approvals: create (pending), get, resolve, double-resolve, list ---
	ap := ApprovalRecord{ID: "ap-1", RunID: "run-alpha", StepIndex: 1, Tool: "mcp://shell",
		SideEffect: "write", Arguments: map[string]any{"cmd": "rm"}, Status: ApprovalPending,
		CreatedAt: day1}
	if err := s.CreateApproval(ctx, ap); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	gotAp, err := s.GetApproval(ctx, "ap-1")
	if err != nil || gotAp.Status != ApprovalPending || gotAp.Arguments["cmd"] != "rm" {
		t.Fatalf("GetApproval = %+v (%v)", gotAp, err)
	}
	if err := s.ResolveApproval(ctx, "ap-1", ApprovalApproved, "ok", "alice", day2); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	gotAp, _ = s.GetApproval(ctx, "ap-1")
	if gotAp.Status != ApprovalApproved || gotAp.DecidedBy != "alice" || gotAp.DecidedAt == nil {
		t.Fatalf("approval not resolved: %+v", gotAp)
	}
	if err := s.ResolveApproval(ctx, "ap-1", ApprovalDenied, "", "", day2); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-resolve = %v, want ErrNotFound", err)
	}
	pend, err := s.ListApprovals(ctx, ApprovalPending)
	if err != nil || len(pend) != 0 {
		t.Fatalf("ListApprovals(pending) = %d (%v), want 0", len(pend), err)
	}
	if alln, _ := s.ListApprovals(ctx, ""); len(alln) != 1 {
		t.Fatalf("ListApprovals(all) = %d, want 1", len(alln))
	}

	// --- policies: upsert, get, update preserves created_at, list ---
	pol := PolicyRecord{Name: "p1", BudgetTokens: 500, BudgetDollars: 2, BudgetLoops: 5, BudgetSeconds: 30,
		ToolAllowlist: []string{"mcp://github"}, ApprovalRules: []ApprovalRule{{Tool: "mcp://shell"}},
		CreatedAt: day1, UpdatedAt: day1}
	if err := s.UpsertPolicy(ctx, pol); err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	pol.BudgetTokens = 999
	pol.UpdatedAt = day2
	pol.CreatedAt = day2 // caller passes a new created_at, but the store must keep the original
	if err := s.UpsertPolicy(ctx, pol); err != nil {
		t.Fatalf("UpsertPolicy update: %v", err)
	}
	gotPol, err := s.GetPolicy(ctx, "p1")
	if err != nil || gotPol.BudgetTokens != 999 || len(gotPol.ToolAllowlist) != 1 ||
		len(gotPol.ApprovalRules) != 1 || gotPol.ApprovalRules[0].Tool != "mcp://shell" {
		t.Fatalf("GetPolicy = %+v (%v)", gotPol, err)
	}
	if !gotPol.CreatedAt.Equal(day1) {
		t.Errorf("policy created_at = %v, want preserved %v", gotPol.CreatedAt, day1)
	}
	if pols, _ := s.ListPolicies(ctx); len(pols) != 1 {
		t.Fatalf("ListPolicies = %d, want 1", len(pols))
	}

	// --- checkpoints: save x2, latest, list ---
	mustCheckpoint(t, s, CheckpointRecord{RunID: "run-alpha", StepIndex: 1, Name: "cp1",
		UsagePromptTokens: 100, UsageDollars: 0.01, UsageLoops: 1,
		Payload: map[string]any{"msg": "first"}, CreatedAt: day1})
	mustCheckpoint(t, s, CheckpointRecord{RunID: "run-alpha", StepIndex: 2, Name: "cp2",
		UsagePromptTokens: 200, UsageDollars: 0.02, UsageLoops: 2,
		Payload: map[string]any{"msg": "second"}, CreatedAt: day2})
	latest, err := s.LatestCheckpoint(ctx, "run-alpha")
	if err != nil || latest.Name != "cp2" || latest.StepIndex != 2 || latest.Payload["msg"] != "second" {
		t.Fatalf("LatestCheckpoint = %+v (%v)", latest, err)
	}
	cps, err := s.ListCheckpoints(ctx, "run-alpha")
	if err != nil || len(cps) != 2 || cps[0].Name != "cp1" {
		t.Fatalf("ListCheckpoints = %+v (%v)", cps, err)
	}

	// --- ErrNotFound on misses ---
	if _, err := s.GetRun(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun miss = %v, want ErrNotFound", err)
	}
	if _, err := s.GetFact(ctx, "ns", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetFact miss = %v, want ErrNotFound", err)
	}
	if _, err := s.GetApproval(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetApproval miss = %v, want ErrNotFound", err)
	}
	if _, err := s.GetPolicy(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPolicy miss = %v, want ErrNotFound", err)
	}
	if _, err := s.LatestCheckpoint(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("LatestCheckpoint miss = %v, want ErrNotFound", err)
	}
}

func mustLedger(t *testing.T, s Store, e LedgerEntry) {
	t.Helper()
	if err := s.AppendLedger(context.Background(), e); err != nil {
		t.Fatalf("AppendLedger: %v", err)
	}
}

func mustCheckpoint(t *testing.T, s Store, c CheckpointRecord) {
	t.Helper()
	if err := s.SaveCheckpoint(context.Background(), c); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
}

// TestSQLiteConformance runs the shared Store conformance suite against SQLite, so
// the suite itself is exercised in ordinary CI and stays honest about parity.
func TestSQLiteConformance(t *testing.T) {
	storeConformance(t, openTemp(t))
}
