package governor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// asHalt asserts err is a *HaltError with the wanted reason.
func asHalt(t *testing.T, err error, want HaltReason) *HaltError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected *HaltError(%s), got nil", want)
	}
	var he *HaltError
	if !errors.As(err, &he) {
		t.Fatalf("expected *HaltError, got %T: %v", err, err)
	}
	if he.Reason != want {
		t.Fatalf("halt reason = %q, want %q", he.Reason, want)
	}
	return he
}

func TestTokenBudget(t *testing.T) {
	r := New(context.Background(), Budget{Tokens: 1000})
	defer r.Close()

	// Under budget: fine.
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed under budget: %v", err)
	}
	if err := r.RecordUsage(400, 100, 0); err != nil {
		t.Fatalf("record 500: %v", err)
	}
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed at 500/1000: %v", err)
	}
	// This call overshoots to exactly 1000 -> halts (hard ceiling reached).
	asHalt(t, r.RecordUsage(400, 100, 0), HaltTokenBudget)
	// Once halted, no further work may start.
	asHalt(t, r.CanProceed(), HaltTokenBudget)
	asHalt(t, r.PreStep(), HaltTokenBudget)
}

func TestTokenBudget_OvershootInOneCall(t *testing.T) {
	// A single call cannot be predicted; the ceiling is enforced by refusing the
	// NEXT call, but the breaching call still halts immediately after recording.
	r := New(context.Background(), Budget{Tokens: 100})
	defer r.Close()
	asHalt(t, r.RecordUsage(1000, 1000, 0), HaltTokenBudget)
	u, reason := r.Status()
	if reason != HaltTokenBudget || u.Tokens() != 2000 {
		t.Fatalf("status = %+v %q", u, reason)
	}
}

func TestDollarBudget(t *testing.T) {
	r := New(context.Background(), Budget{Dollars: 5.00})
	defer r.Close()
	if err := r.RecordUsage(0, 0, 4.99); err != nil {
		t.Fatalf("under dollar budget: %v", err)
	}
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed at 4.99/5.00: %v", err)
	}
	asHalt(t, r.RecordUsage(0, 0, 0.01), HaltDollarBudget)
}

func TestLoopBudget_AllowsExactlyN(t *testing.T) {
	r := New(context.Background(), Budget{Loops: 3})
	defer r.Close()
	for i := 0; i < 3; i++ {
		if err := r.PreStep(); err != nil {
			t.Fatalf("PreStep iter %d should succeed: %v", i, err)
		}
	}
	// 4th iteration is refused.
	asHalt(t, r.PreStep(), HaltLoopBudget)
	u, _ := r.Status()
	if u.Loops != 3 {
		t.Fatalf("loops = %d, want 3", u.Loops)
	}
}

func TestLoopBudget_FinalIterationCanRecordUsage(t *testing.T) {
	// Regression: loop budget must not be enforced in checkLocked, or the last
	// allowed iteration could not record its own usage.
	r := New(context.Background(), Budget{Loops: 1, Tokens: 10_000})
	defer r.Close()
	if err := r.PreStep(); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed in final iter: %v", err)
	}
	if err := r.RecordUsage(100, 100, 0.01); err != nil {
		t.Fatalf("RecordUsage in final iter must succeed: %v", err)
	}
}

func TestTimeBudget_InjectedClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	r := New(context.Background(), Budget{Seconds: 30}, WithClock(clock))
	defer r.Close()

	if err := r.PreStep(); err != nil {
		t.Fatalf("PreStep at t=0: %v", err)
	}
	// Advance 29s: still inside budget.
	now = now.Add(29 * time.Second)
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed at 29s: %v", err)
	}
	// Advance to 30s: budget reached -> halt on both PreStep and CanProceed.
	now = now.Add(1 * time.Second)
	asHalt(t, r.CanProceed(), HaltTimeBudget)
	asHalt(t, r.PreStep(), HaltTimeBudget)
}

func TestTimeBudget_CancelsContext(t *testing.T) {
	now := time.Unix(0, 0)
	r := New(context.Background(), Budget{Seconds: 10}, WithClock(func() time.Time { return now }))
	defer r.Close()
	now = now.Add(10 * time.Second)
	asHalt(t, r.CanProceed(), HaltTimeBudget)
	select {
	case <-r.Context().Done():
	default:
		t.Fatal("context should be cancelled after time-budget halt")
	}
}

func TestKillSwitch(t *testing.T) {
	r := New(context.Background(), Budget{Tokens: 1_000_000})
	defer r.Close()
	r.Cancel()
	if !r.Halted() {
		t.Fatal("Cancel should halt the run")
	}
	asHalt(t, r.CanProceed(), HaltCancelled)
	asHalt(t, r.PreStep(), HaltCancelled)
	select {
	case <-r.Context().Done():
	default:
		t.Fatal("context should be cancelled after kill switch")
	}
}

func TestFirstReasonWins(t *testing.T) {
	r := New(context.Background(), Budget{Tokens: 100})
	defer r.Close()
	asHalt(t, r.RecordUsage(100, 0, 0), HaltTokenBudget)
	// A later Cancel must not overwrite the original halt reason.
	r.Cancel()
	_, reason := r.Status()
	if reason != HaltTokenBudget {
		t.Fatalf("reason overwritten: %q", reason)
	}
}

func TestUnlimitedBudgetNeverHalts(t *testing.T) {
	r := New(context.Background(), Budget{}) // all zero = unlimited
	defer r.Close()
	for i := 0; i < 1000; i++ {
		if err := r.PreStep(); err != nil {
			t.Fatalf("PreStep %d: %v", i, err)
		}
		if err := r.RecordUsage(1_000_000, 1_000_000, 1000); err != nil {
			t.Fatalf("RecordUsage %d: %v", i, err)
		}
	}
	if r.Halted() {
		t.Fatal("unlimited budget should never halt")
	}
}

func TestInteraction_LoopBeforeToken(t *testing.T) {
	// Loop budget trips first (3 iters), even though token budget is generous.
	r := New(context.Background(), Budget{Loops: 3, Tokens: 1_000_000})
	defer r.Close()
	for i := 0; i < 3; i++ {
		if err := r.PreStep(); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		_ = r.RecordUsage(10, 10, 0)
	}
	asHalt(t, r.PreStep(), HaltLoopBudget)
}

func TestInteraction_TokenBeforeLoop(t *testing.T) {
	// Token budget trips mid-iteration before the loop budget would.
	r := New(context.Background(), Budget{Loops: 100, Tokens: 50})
	defer r.Close()
	if err := r.PreStep(); err != nil {
		t.Fatal(err)
	}
	asHalt(t, r.RecordUsage(50, 0, 0), HaltTokenBudget)
}

func TestHaltErrorCarriesUsage(t *testing.T) {
	r := New(context.Background(), Budget{Dollars: 1.0})
	defer r.Close()
	err := r.RecordUsage(120, 30, 1.5)
	he := asHalt(t, err, HaltDollarBudget)
	if he.Usage.Tokens() != 150 || he.Usage.Dollars != 1.5 {
		t.Fatalf("HaltError usage = %+v", he.Usage)
	}
}

// TestConcurrentCancel exercises the kill switch firing from another goroutine
// while guard methods run. Run with -race.
func TestWithRestoredUsage(t *testing.T) {
	// A run reconstructed with prior usage keeps enforcing against the budget it
	// already spent (crash-resume).
	r := New(context.Background(), Budget{Tokens: 1000},
		WithRestoredUsage(Usage{PromptTokens: 600, CompletionTokens: 300, Loops: 3}))
	defer r.Close()

	u, _ := r.Status()
	if u.Tokens() != 900 || u.Loops != 3 {
		t.Fatalf("restored usage = %+v", u)
	}
	if err := r.CanProceed(); err != nil {
		t.Fatalf("CanProceed at 900/1000: %v", err)
	}
	// 200 more crosses the restored budget.
	asHalt(t, r.RecordUsage(200, 0, 0), HaltTokenBudget)
}

func TestConcurrentCancel(t *testing.T) {
	r := New(context.Background(), Budget{Tokens: 1_000_000_000})
	defer r.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = r.PreStep()
				_ = r.CanProceed()
				_ = r.RecordUsage(1, 1, 0.0001)
				_, _ = r.Status()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(time.Millisecond)
		r.Cancel()
	}()
	wg.Wait()

	if !r.Halted() {
		t.Fatal("run should be halted after concurrent cancel")
	}
}
