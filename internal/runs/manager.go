// Package runs manages governed runs — the identity, budget, and lifecycle around
// a governor.Run. It is the shared state used by both the OpenAI-compatible proxy
// (Surface 1) and the public /v1/runs API.
//
// Runs are held in memory for fast live enforcement (the governor is the source
// of truth for limits) and written through to a storage.Store so they are durable
// and auditable, and so a crashed run can be resumed (step 5). Persistence is
// best-effort: a write failure is logged but never fails the user's call, because
// enforcement has already happened in memory. Persistence writes use a background
// context, never the run's context (which is cancelled on halt).
package runs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/id"
	"github.com/prashar32/riskkernel/internal/storage"
)

// Run is a governed run: identity and metadata wrapped around a live governor.
type Run struct {
	ID       string
	Name     string
	Budget   governor.Budget
	Metadata map[string]string

	mgr       *Manager
	gov       *governor.Run
	createdAt time.Time

	mu             sync.Mutex
	updatedAt      time.Time
	curStepStarted time.Time
}

// Call is a completed model call to record against a run — meters the governor
// and appends an auditable cost-ledger entry.
type Call struct {
	StepIndex        int32
	Provider         string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	Dollars          float64
	Priced           bool
	ResponseID       string
}

// View is an immutable snapshot of a run for reporting / API serialization.
type View struct {
	ID         string
	Name       string
	Status     string
	Budget     governor.Budget
	Usage      governor.Usage
	HaltReason governor.HaltReason
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Metadata   map[string]string
}

// BeginStep registers a new loop iteration (enforcing loop + time budgets) and
// returns the 1-based step index. Returns a *governor.HaltError if the run can't
// start another step.
func (r *Run) BeginStep() (int32, error) {
	if err := r.gov.PreStep(); err != nil {
		return 0, err
	}
	now := time.Now()
	u, _ := r.gov.Status()
	r.mu.Lock()
	r.updatedAt = now
	r.curStepStarted = now
	r.mu.Unlock()

	r.mgr.persistStepStart(r, u.Loops, now)
	return u.Loops, nil
}

// CanProceed enforces the hard ceiling immediately before a model/tool call.
func (r *Run) CanProceed() error { return r.gov.CanProceed() }

// RecordCall meters a completed model call in the governor and writes through to
// the cost ledger + step + run rows. Returns a *governor.HaltError if this call
// crossed a budget.
func (r *Run) RecordCall(c Call) error {
	err := r.gov.RecordUsage(c.PromptTokens, c.CompletionTokens, c.Dollars)
	r.touch()
	r.mgr.persistCall(r, c, err)
	return err
}

// Cancel is the kill switch for this run.
func (r *Run) Cancel() {
	r.gov.Cancel()
	r.touch()
	r.mgr.persistRun(r)
}

// Context returns the run's governance context (cancelled on halt/kill/time).
func (r *Run) Context() context.Context { return r.gov.Context() }

// Halted reports whether the run has stopped.
func (r *Run) Halted() bool { return r.gov.Halted() }

// HaltReason returns the current halt reason (HaltNone if running).
func (r *Run) HaltReason() governor.HaltReason {
	_, h := r.gov.Status()
	return h
}

// Close releases the run's context resources.
func (r *Run) Close() { r.gov.Close() }

// View returns a snapshot for reporting.
func (r *Run) View() View {
	r.mu.Lock()
	updated := r.updatedAt
	r.mu.Unlock()

	usage, halt := r.gov.Status()
	return View{
		ID:         r.ID,
		Name:       r.Name,
		Status:     statusFor(halt),
		Budget:     r.Budget,
		Usage:      usage,
		HaltReason: halt,
		CreatedAt:  r.createdAt,
		UpdatedAt:  updated,
		Metadata:   r.Metadata,
	}
}

// record builds the persisted form of the run from its current state.
func (r *Run) record() storage.RunRecord {
	v := r.View()
	return storage.RunRecord{
		ID:                    v.ID,
		Name:                  v.Name,
		Status:                v.Status,
		HaltReason:            string(v.HaltReason),
		BudgetTokens:          v.Budget.Tokens,
		BudgetDollars:         v.Budget.Dollars,
		BudgetLoops:           v.Budget.Loops,
		BudgetSeconds:         v.Budget.Seconds,
		UsagePromptTokens:     v.Usage.PromptTokens,
		UsageCompletionTokens: v.Usage.CompletionTokens,
		UsageDollars:          v.Usage.Dollars,
		UsageLoops:            v.Usage.Loops,
		Metadata:              v.Metadata,
		CreatedAt:             v.CreatedAt,
		UpdatedAt:             v.UpdatedAt,
	}
}

func (r *Run) touch() {
	now := time.Now()
	r.mu.Lock()
	r.updatedAt = now
	r.mu.Unlock()
}

func statusFor(h governor.HaltReason) string {
	switch h {
	case governor.HaltNone:
		return "running"
	case governor.HaltCancelled:
		return "cancelled"
	default:
		return "halted"
	}
}

// Manager owns the set of live runs. Safe for concurrent use.
type Manager struct {
	defaultBudget governor.Budget
	store         storage.Store // nil → in-memory only
	log           *slog.Logger

	mu   sync.RWMutex
	runs map[string]*Run
}

// NewManager constructs an in-memory Manager. defaultBudget is applied to runs
// created without an explicit budget. Attach durability with WithStore.
func NewManager(defaultBudget governor.Budget) *Manager {
	return &Manager{
		defaultBudget: defaultBudget,
		log:           slog.New(slog.NewTextHandler(noopWriter{}, nil)),
		runs:          make(map[string]*Run),
	}
}

// WithStore attaches a durable Store and logger for write-through persistence and
// returns the Manager for chaining.
func (m *Manager) WithStore(store storage.Store, log *slog.Logger) *Manager {
	m.store = store
	if log != nil {
		m.log = log
	}
	return m
}

// Store returns the attached Store (nil if in-memory only).
func (m *Manager) Store() storage.Store { return m.store }

// CreateOptions configures a new run.
type CreateOptions struct {
	ID       string // optional; a UUID is minted when empty
	Name     string
	Budget   *governor.Budget // nil → manager default
	Metadata map[string]string
}

// Create starts a new governed run and registers it.
func (m *Manager) Create(opts CreateOptions) *Run {
	budget := m.defaultBudget
	if opts.Budget != nil {
		budget = *opts.Budget
	}
	rid := opts.ID
	if rid == "" {
		rid = id.NewUUID()
	}
	r := m.newRun(rid, opts.Name, budget, opts.Metadata)
	m.mu.Lock()
	m.runs[rid] = r
	m.mu.Unlock()
	m.persistRun(r)
	return r
}

// Get returns the run with the given id.
func (m *Manager) Get(rid string) (*Run, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runs[rid]
	return r, ok
}

// GetOrCreate returns the run with the given id, lazily creating it under the
// default budget if unknown. This is the proxy path: a client groups calls into a
// run by sending a stable run-id header; the first call materializes the run.
func (m *Manager) GetOrCreate(rid string) *Run {
	if r, ok := m.Get(rid); ok {
		return r
	}
	m.mu.Lock()
	if r, ok := m.runs[rid]; ok { // re-check under write lock
		m.mu.Unlock()
		return r
	}
	r := m.newRun(rid, "", m.defaultBudget, nil)
	m.runs[rid] = r
	m.mu.Unlock()
	m.persistRun(r)
	return r
}

// List returns snapshots of all runs.
func (m *Manager) List() []View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]View, 0, len(m.runs))
	for _, r := range m.runs {
		out = append(out, r.View())
	}
	return out
}

func (m *Manager) newRun(rid, name string, budget governor.Budget, meta map[string]string) *Run {
	now := time.Now()
	return &Run{
		ID:        rid,
		Name:      name,
		Budget:    budget,
		Metadata:  meta,
		mgr:       m,
		gov:       governor.New(context.Background(), budget),
		createdAt: now,
		updatedAt: now,
	}
}

// --- write-through persistence (best-effort; background context) ---

func (m *Manager) persistRun(r *Run) {
	if m.store == nil {
		return
	}
	if err := m.store.UpsertRun(context.Background(), r.record()); err != nil {
		m.log.Error("persist run failed", "run", r.ID, "err", err)
	}
}

func (m *Manager) persistStepStart(r *Run, idx int32, started time.Time) {
	if m.store == nil {
		return
	}
	if err := m.store.UpsertStep(context.Background(), storage.StepRecord{
		RunID: r.ID, Index: idx, Status: "running", StartedAt: started,
	}); err != nil {
		m.log.Error("persist step start failed", "run", r.ID, "step", idx, "err", err)
	}
	m.persistRun(r)
}

func (m *Manager) persistCall(r *Run, c Call, haltErr error) {
	if m.store == nil {
		return
	}
	ctx := context.Background()
	now := time.Now()

	if err := m.store.AppendLedger(ctx, storage.LedgerEntry{
		RunID: r.ID, StepIndex: c.StepIndex, Provider: c.Provider, Model: c.Model,
		PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens,
		Dollars: c.Dollars, Priced: c.Priced, ResponseID: c.ResponseID, CreatedAt: now,
	}); err != nil {
		m.log.Error("persist ledger failed", "run", r.ID, "err", err)
	}

	r.mu.Lock()
	started := r.curStepStarted
	r.mu.Unlock()
	stepStatus := "completed"
	if haltErr != nil {
		stepStatus = "halted"
	}
	if err := m.store.UpsertStep(ctx, storage.StepRecord{
		RunID: r.ID, Index: c.StepIndex, Status: stepStatus,
		PromptTokens: c.PromptTokens, CompletionTokens: c.CompletionTokens, Dollars: c.Dollars,
		StartedAt: started, EndedAt: &now,
	}); err != nil {
		m.log.Error("persist step failed", "run", r.ID, "step", c.StepIndex, "err", err)
	}

	m.persistRun(r)
}

// noopWriter discards log output for the default in-memory manager.
type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
