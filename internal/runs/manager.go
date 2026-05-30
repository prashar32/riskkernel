// Package runs manages governed runs — the identity, budget, and lifecycle around
// a governor.Run. It is the shared state used by both the OpenAI-compatible proxy
// (Surface 1) and the public /v1/runs API. v0.1 keeps runs in memory; step 4
// backs this with the SQLite Store behind the same surface (durable runs +
// crash-resume) without changing callers.
package runs

import (
	"context"
	"sync"
	"time"

	"github.com/prashar32/riskkernel/internal/governor"
	"github.com/prashar32/riskkernel/internal/id"
)

// Run is a governed run: identity and metadata wrapped around a live governor.
type Run struct {
	ID       string
	Name     string
	Budget   governor.Budget
	Metadata map[string]string

	gov       *governor.Run
	createdAt time.Time

	mu        sync.Mutex
	updatedAt time.Time
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
	r.touch()
	u, _ := r.gov.Status()
	return u.Loops, nil
}

// CanProceed enforces the hard ceiling immediately before a model/tool call.
func (r *Run) CanProceed() error { return r.gov.CanProceed() }

// RecordUsage adds a completed call's usage and re-evaluates budgets.
func (r *Run) RecordUsage(promptTokens, completionTokens int64, dollars float64) error {
	err := r.gov.RecordUsage(promptTokens, completionTokens, dollars)
	r.touch()
	return err
}

// Cancel is the kill switch for this run.
func (r *Run) Cancel() {
	r.gov.Cancel()
	r.touch()
}

// Context returns the run's governance context (cancelled on halt/kill/time).
// Pass it to provider/tool calls so enforcement reaches work in flight.
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

	mu   sync.RWMutex
	runs map[string]*Run
}

// NewManager constructs a Manager. defaultBudget is applied to runs created
// without an explicit budget (e.g. proxy calls that supply only a run id).
func NewManager(defaultBudget governor.Budget) *Manager {
	return &Manager{defaultBudget: defaultBudget, runs: make(map[string]*Run)}
}

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
	now := time.Now()
	r := &Run{
		ID:        rid,
		Name:      opts.Name,
		Budget:    budget,
		Metadata:  opts.Metadata,
		gov:       governor.New(context.Background(), budget),
		createdAt: now,
		updatedAt: now,
	}
	m.mu.Lock()
	m.runs[rid] = r
	m.mu.Unlock()
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
	defer m.mu.Unlock()
	if r, ok := m.runs[rid]; ok { // re-check under write lock
		return r
	}
	now := time.Now()
	r := &Run{
		ID:        rid,
		Budget:    m.defaultBudget,
		gov:       governor.New(context.Background(), m.defaultBudget),
		createdAt: now,
		updatedAt: now,
	}
	m.runs[rid] = r
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
