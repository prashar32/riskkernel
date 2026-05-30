// Package governor is RiskKernel's deterministic disposer — the headline feature.
// It enforces hard per-run budgets (tokens, dollars, loop iterations, wall-clock
// time) and a kill switch around every model and tool call. No LLM is ever in
// this path: the agent proposes, the governor disposes.
//
// The contract is simple and strict:
//   - PreStep() is called before each loop iteration; it enforces the loop and
//     time budgets and refuses to start an iteration the budget can't afford.
//   - CanProceed() is called immediately before a model/tool call; it enforces a
//     hard ceiling — a run never STARTS work once any budget is breached.
//   - RecordUsage() is called after a call; it adds to the ledger and halts the
//     run the instant a budget is crossed.
//   - Cancel() is the external kill switch.
//
// Once halted, every guard method returns *HaltError and the derived Context is
// cancelled, interrupting any in-flight provider call. A Run is safe for
// concurrent use: the kill switch may fire from another goroutine while a call is
// in flight.
package governor

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Budget is the set of hard per-run limits. A zero value in any field means
// "unlimited" for that dimension. Mirrors the api/v1 Budget schema.
type Budget struct {
	Tokens  int64   // max total tokens (prompt + completion)
	Dollars float64 // max USD cost
	Loops   int32   // max loop iterations
	Seconds int32   // max wall-clock seconds
}

// Usage is the running total a Run has consumed.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	Dollars          float64
	Loops            int32
}

// Tokens returns total tokens consumed.
func (u Usage) Tokens() int64 { return u.PromptTokens + u.CompletionTokens }

// HaltReason is the machine-readable reason a run stopped. Values mirror the
// api/v1 HaltReason enum so the API layer can pass them through unchanged.
type HaltReason string

const (
	HaltNone         HaltReason = ""
	HaltTokenBudget  HaltReason = "token_budget_exceeded"
	HaltDollarBudget HaltReason = "dollar_budget_exceeded"
	HaltLoopBudget   HaltReason = "loop_budget_exceeded"
	HaltTimeBudget   HaltReason = "time_budget_exceeded"
	HaltCancelled    HaltReason = "cancelled"
)

// HaltError is returned by every guard method once a run is halted. Callers stop
// their loop on this error and persist final state.
type HaltError struct {
	Reason HaltReason
	Usage  Usage
}

func (e *HaltError) Error() string {
	return fmt.Sprintf("run halted: %s (tokens=%d dollars=%.4f loops=%d)",
		e.Reason, e.Usage.Tokens(), e.Usage.Dollars, e.Usage.Loops)
}

// Run is the live governance state for a single governed run.
type Run struct {
	budget    Budget
	now       func() time.Time
	startedAt time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	usage  Usage
	halted HaltReason
}

// Option configures a Run.
type Option func(*Run)

// WithClock injects a clock for deterministic time-budget tests. Production uses
// time.Now. The injected clock drives the logical time-budget check; the derived
// Context's real-time backstop (which interrupts in-flight calls) is unaffected.
func WithClock(now func() time.Time) Option {
	return func(r *Run) { r.now = now }
}

// WithRestoredUsage seeds a run's accumulated usage — used by crash-resume to
// reconstruct a run so it keeps enforcing against the budget it already spent
// before the daemon restarted. The wall-clock budget restarts its clock on
// resume (it meters a single active session, not downtime).
func WithRestoredUsage(u Usage) Option {
	return func(r *Run) { r.usage = u }
}

// New starts governing a run under budget b. The returned Run derives a Context
// (from parent) that is cancelled on halt/cancel, and that additionally carries a
// real-time deadline when a time budget is set — so an in-flight provider call is
// interrupted, not just refused at the next checkpoint. Call Close when done.
func New(parent context.Context, b Budget, opts ...Option) *Run {
	if parent == nil {
		parent = context.Background()
	}
	r := &Run{budget: b, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	r.startedAt = r.now()

	if b.Seconds > 0 {
		// Real-time backstop independent of any injected clock: guarantees an
		// in-flight call cannot outlive the time budget.
		r.ctx, r.cancel = context.WithTimeout(parent, time.Duration(b.Seconds)*time.Second)
	} else {
		r.ctx, r.cancel = context.WithCancel(parent)
	}
	return r
}

// Context returns the run's context. Pass it to every provider/tool call so the
// kill switch and time budget can interrupt work in flight.
func (r *Run) Context() context.Context { return r.ctx }

// Close releases the context resources. Safe to call multiple times. It does not
// itself halt the run's accounting; use Cancel for the kill switch.
func (r *Run) Close() {
	if r.cancel != nil {
		r.cancel()
	}
}

// PreStep registers the start of a new loop iteration. Call it before doing the
// work of a step. It enforces the time and loop budgets and refuses to start an
// iteration the budget can't afford. Returns *HaltError if the run is (now)
// halted; the caller must stop the loop.
func (r *Run) PreStep() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.halted != HaltNone {
		return r.haltErrLocked()
	}
	if r.expiredLocked() {
		return r.haltLocked(HaltTimeBudget)
	}
	if r.budget.Loops > 0 && r.usage.Loops+1 > r.budget.Loops {
		return r.haltLocked(HaltLoopBudget)
	}
	r.usage.Loops++
	return nil
}

// CanProceed enforces the hard ceiling immediately before a model/tool call: a
// run never starts work once a token, dollar, or time budget is breached. Returns
// *HaltError if the run cannot proceed.
func (r *Run) CanProceed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkLocked()
}

// RecordUsage adds a completed call's usage to the ledger and re-evaluates
// budgets. Returns *HaltError if this usage crossed a token, dollar, or time
// budget — the run is then halted and its Context cancelled.
func (r *Run) RecordUsage(promptTokens, completionTokens int64, dollars float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage.PromptTokens += promptTokens
	r.usage.CompletionTokens += completionTokens
	r.usage.Dollars += dollars
	return r.checkLocked()
}

// Cancel is the external kill switch. It halts the run (reason "cancelled") and
// cancels the Context, interrupting any in-flight call. Idempotent: a run already
// halted keeps its original reason.
func (r *Run) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.haltLocked(HaltCancelled)
}

// Status returns a snapshot of usage and the current halt reason (HaltNone if
// running).
func (r *Run) Status() (Usage, HaltReason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage, r.halted
}

// Halted reports whether the run has stopped.
func (r *Run) Halted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.halted != HaltNone
}

// Budget returns the run's configured budget.
func (r *Run) Budget() Budget { return r.budget }

// Elapsed returns the logical wall-clock time since the run started, per the
// run's clock.
func (r *Run) Elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now().Sub(r.startedAt)
}

// --- locked internals (caller holds r.mu) ---

// checkLocked evaluates the token, dollar, and time budgets against current
// usage. Loop budget is enforced only in PreStep (before an iteration starts), so
// it is intentionally absent here — otherwise the final allowed iteration could
// not record its own usage. Flips to halted and returns the error if breached.
func (r *Run) checkLocked() error {
	if r.halted != HaltNone {
		return r.haltErrLocked()
	}
	if r.expiredLocked() {
		return r.haltLocked(HaltTimeBudget)
	}
	if r.budget.Tokens > 0 && r.usage.Tokens() >= r.budget.Tokens {
		return r.haltLocked(HaltTokenBudget)
	}
	if r.budget.Dollars > 0 && r.usage.Dollars >= r.budget.Dollars {
		return r.haltLocked(HaltDollarBudget)
	}
	return nil
}

func (r *Run) expiredLocked() bool {
	if r.budget.Seconds <= 0 {
		return false
	}
	return r.now().Sub(r.startedAt) >= time.Duration(r.budget.Seconds)*time.Second
}

// haltLocked records the halt reason (first reason wins) and fires the kill
// switch, then returns the halt error.
func (r *Run) haltLocked(reason HaltReason) error {
	if r.halted == HaltNone {
		r.halted = reason
		if r.cancel != nil {
			r.cancel()
		}
	}
	return r.haltErrLocked()
}

func (r *Run) haltErrLocked() error {
	return &HaltError{Reason: r.halted, Usage: r.usage}
}
