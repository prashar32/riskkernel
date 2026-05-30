package runs

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/storage"
)

func TestManager_InMemory(t *testing.T) {
	m := NewManager(governor.Budget{Loops: 2})
	r := m.Create(CreateOptions{Name: "x"})
	if _, ok := m.Get(r.ID); !ok {
		t.Fatal("run not registered")
	}
	if _, err := r.BeginStep(); err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if _, err := r.BeginStep(); err != nil {
		t.Fatalf("step 2: %v", err)
	}
	if _, err := r.BeginStep(); err == nil {
		t.Fatal("step 3 should hit loop budget")
	}
	if len(m.List()) != 1 {
		t.Fatalf("List = %d", len(m.List()))
	}
}

func TestManager_WriteThroughPersistence(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := NewManager(governor.Budget{Tokens: 1000}).
		WithStore(store, slog.New(slog.NewTextHandler(noopWriter{}, nil)))

	r := m.Create(CreateOptions{ID: "run-x", Name: "demo", Metadata: map[string]string{"k": "v"}})

	step, err := r.BeginStep()
	if err != nil {
		t.Fatalf("BeginStep: %v", err)
	}
	// Record a call that stays under budget.
	if err := r.RecordCall(Call{
		StepIndex: step, Provider: "anthropic", Model: "claude-sonnet-4-5",
		PromptTokens: 100, CompletionTokens: 50, Dollars: 0.012, Priced: true, ResponseID: "resp-1",
	}); err != nil {
		t.Fatalf("RecordCall: %v", err)
	}

	ctx := context.Background()

	// Run row persisted with current usage.
	got, err := store.GetRun(ctx, "run-x")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Name != "demo" || got.Status != "running" || got.Metadata["k"] != "v" {
		t.Fatalf("run row = %+v", got)
	}
	if got.UsagePromptTokens != 100 || got.UsageCompletionTokens != 50 || got.UsageLoops != 1 {
		t.Fatalf("usage not persisted: %+v", got)
	}

	// Step row persisted and completed.
	steps, err := store.ListSteps(ctx, "run-x")
	if err != nil || len(steps) != 1 {
		t.Fatalf("ListSteps = %v, %v", steps, err)
	}
	if steps[0].Status != "completed" || steps[0].EndedAt == nil || steps[0].CompletionTokens != 50 {
		t.Fatalf("step row = %+v", steps[0])
	}

	// Ledger entry persisted.
	ledger, err := store.LedgerForRun(ctx, "run-x")
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger = %v, %v", ledger, err)
	}
	if ledger[0].Model != "claude-sonnet-4-5" || ledger[0].ResponseID != "resp-1" {
		t.Fatalf("ledger entry = %+v", ledger[0])
	}

	totals, _ := store.Totals(ctx, "run-x")
	if totals.Calls != 1 || totals.PromptTokens != 100 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestManager_ReloadResumesBudget(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	noop := slog.New(slog.NewTextHandler(noopWriter{}, nil))

	// Process A: a run with a 1000-token budget spends 900.
	mA := NewManager(governor.Budget{Tokens: 1000}).WithStore(store, noop)
	rA := mA.Create(CreateOptions{ID: "resume-me", Name: "long-run"})
	step, _ := rA.BeginStep()
	if err := rA.RecordCall(Call{StepIndex: step, Provider: "anthropic", Model: "m",
		PromptTokens: 600, CompletionTokens: 300}); err != nil {
		t.Fatalf("first call (900 tokens) should be under budget: %v", err)
	}

	// --- simulate SIGKILL: drop manager A, keep the store ---

	// Process B starts with a DIFFERENT default budget (unlimited). If reload
	// restored the manager default instead of the run's own budget, the next call
	// would NOT halt — so this proves the per-run budget is what's restored.
	mB := NewManager(governor.Budget{}).WithStore(store, noop)
	n, err := mB.Reload(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Reload = %d, %v; want 1", n, err)
	}
	rB, ok := mB.Get("resume-me")
	if !ok {
		t.Fatal("run not reloaded")
	}
	if v := rB.View(); v.Usage.Tokens() != 900 || v.Usage.Loops != 1 {
		t.Fatalf("restored usage = %+v", v.Usage)
	}

	// A further 200-token call crosses the restored 1000 budget → halt.
	step2, _ := rB.BeginStep()
	err = rB.RecordCall(Call{StepIndex: step2, Provider: "anthropic", Model: "m",
		PromptTokens: 200, CompletionTokens: 0})
	if err == nil {
		t.Fatal("call after resume should hit the restored token budget")
	}
	got, _ := store.GetRun(context.Background(), "resume-me")
	if got.HaltReason != string(governor.HaltTokenBudget) {
		t.Fatalf("halt reason = %q", got.HaltReason)
	}
}

func TestManager_ReloadSkipsTerminalRuns(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "term.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	noop := slog.New(slog.NewTextHandler(noopWriter{}, nil))

	mA := NewManager(governor.Budget{}).WithStore(store, noop)
	running := mA.Create(CreateOptions{ID: "still-running"})
	_ = running
	cancelled := mA.Create(CreateOptions{ID: "was-cancelled"})
	cancelled.Cancel()

	mB := NewManager(governor.Budget{}).WithStore(store, noop)
	n, err := mB.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reloaded %d runs, want 1 (only the running one)", n)
	}
	if _, ok := mB.Get("was-cancelled"); ok {
		t.Error("cancelled run should not be reloaded as live")
	}
	if _, ok := mB.Get("still-running"); !ok {
		t.Error("running run should be reloaded")
	}
}

func TestManager_HaltPersistsStatus(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "halt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	m := NewManager(governor.Budget{Tokens: 100}).
		WithStore(store, slog.New(slog.NewTextHandler(noopWriter{}, nil)))
	r := m.Create(CreateOptions{ID: "run-h"})

	step, _ := r.BeginStep()
	// This call exceeds the 100-token budget → halts.
	if err := r.RecordCall(Call{StepIndex: step, Provider: "anthropic", Model: "m",
		PromptTokens: 200, CompletionTokens: 0, Dollars: 0}); err == nil {
		t.Fatal("expected halt error")
	}

	got, _ := store.GetRun(context.Background(), "run-h")
	if got.Status != "halted" || got.HaltReason != string(governor.HaltTokenBudget) {
		t.Fatalf("halt not persisted: status=%q reason=%q", got.Status, got.HaltReason)
	}
	steps, _ := store.ListSteps(context.Background(), "run-h")
	if len(steps) != 1 || steps[0].Status != "halted" {
		t.Fatalf("halted step not persisted: %+v", steps)
	}
}
