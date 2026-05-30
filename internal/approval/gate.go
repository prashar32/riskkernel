package approval

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prashar32/riskkernel/internal/id"
	"github.com/prashar32/riskkernel/internal/storage"
)

// Decision is the outcome of an approval request.
type Decision struct {
	Approved bool
	Reason   string
	By       string
}

// Request describes a side-effecting tool call seeking approval.
type Request struct {
	RunID      string
	StepIndex  int32
	Tool       string
	SideEffect string
	Arguments  map[string]any
}

// Notifier pushes a newly-pending approval to a channel (e.g. a webhook). The CLI
// and web page are pull channels and do not implement this.
type Notifier interface {
	Notify(ctx context.Context, a storage.ApprovalRecord)
}

// Gate is the human-in-the-loop approval queue. It persists pending approvals,
// notifies push channels, and blocks the calling (tool) goroutine until a human
// resolves the request via the API/CLI/web — or the run's context is cancelled.
type Gate struct {
	store    storage.Store
	policy   Policy
	notifier Notifier
	log      *slog.Logger
	now      func() time.Time
	newID    func() string

	mu      sync.Mutex
	waiters map[string]chan Decision
}

// NewGate constructs a Gate. notifier may be nil (no push channel).
func NewGate(store storage.Store, policy Policy, notifier Notifier, log *slog.Logger) *Gate {
	return &Gate{
		store:    store,
		policy:   policy,
		notifier: notifier,
		log:      log,
		now:      time.Now,
		newID:    id.NewUUID,
		waiters:  make(map[string]chan Decision),
	}
}

// Required reports whether a call needs approval under the gate's policy.
func (g *Gate) Required(tool, sideEffect string) bool {
	return g.policy.Requires(tool, sideEffect)
}

// Request gates a side-effecting call. If approval is not required it returns an
// approved Decision immediately. Otherwise it persists a pending approval,
// notifies push channels, and BLOCKS until the request is resolved or ctx is
// done (run cancel / time budget). This is how a side-effecting call "pauses".
func (g *Gate) Request(ctx context.Context, req Request) (Decision, string, error) {
	if !g.policy.Requires(req.Tool, req.SideEffect) {
		return Decision{Approved: true}, "", nil
	}

	rec := storage.ApprovalRecord{
		ID:         g.newID(),
		RunID:      req.RunID,
		StepIndex:  req.StepIndex,
		Tool:       req.Tool,
		SideEffect: req.SideEffect,
		Arguments:  req.Arguments,
		Status:     storage.ApprovalPending,
		CreatedAt:  g.now(),
	}
	if g.store != nil {
		if err := g.store.CreateApproval(ctx, rec); err != nil {
			return Decision{}, "", err
		}
	}

	ch := make(chan Decision, 1)
	g.mu.Lock()
	g.waiters[rec.ID] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.waiters, rec.ID)
		g.mu.Unlock()
	}()

	if g.notifier != nil {
		g.notifier.Notify(ctx, rec)
	}
	g.log.Info("approval required", "id", rec.ID, "run", rec.RunID, "tool", rec.Tool, "side_effect", rec.SideEffect)

	select {
	case d := <-ch:
		return d, rec.ID, nil
	case <-ctx.Done():
		return Decision{}, rec.ID, ctx.Err()
	}
}

// Create evaluates policy and, if approval is required, persists a pending
// approval and notifies push channels, returning the record (required=true).
// Unlike Request, it does NOT block — the caller (e.g. the SDK over HTTP) polls
// the approval's status instead. Returns required=false when policy allows the
// call outright.
func (g *Gate) Create(ctx context.Context, req Request) (storage.ApprovalRecord, bool, error) {
	if !g.policy.Requires(req.Tool, req.SideEffect) {
		return storage.ApprovalRecord{}, false, nil
	}
	rec := storage.ApprovalRecord{
		ID:         g.newID(),
		RunID:      req.RunID,
		StepIndex:  req.StepIndex,
		Tool:       req.Tool,
		SideEffect: req.SideEffect,
		Arguments:  req.Arguments,
		Status:     storage.ApprovalPending,
		CreatedAt:  g.now(),
	}
	if g.store != nil {
		if err := g.store.CreateApproval(ctx, rec); err != nil {
			return storage.ApprovalRecord{}, false, err
		}
	}
	if g.notifier != nil {
		g.notifier.Notify(ctx, rec)
	}
	g.log.Info("approval required", "id", rec.ID, "run", rec.RunID, "tool", rec.Tool, "side_effect", rec.SideEffect)
	return rec, true, nil
}

// Resolve records a human decision and wakes any blocked waiter. Returns
// storage.ErrNotFound if the approval is unknown or already resolved.
func (g *Gate) Resolve(ctx context.Context, id string, approved bool, reason, by string) error {
	status := storage.ApprovalApproved
	if !approved {
		status = storage.ApprovalDenied
	}
	if g.store != nil {
		if err := g.store.ResolveApproval(ctx, id, status, reason, by, g.now()); err != nil {
			return err
		}
	}
	g.mu.Lock()
	ch := g.waiters[id]
	g.mu.Unlock()
	if ch != nil {
		ch <- Decision{Approved: approved, Reason: reason, By: by}
	}
	return nil
}

// Pending returns the currently-pending approvals.
func (g *Gate) Pending(ctx context.Context) ([]storage.ApprovalRecord, error) {
	if g.store == nil {
		return nil, nil
	}
	return g.store.ListApprovals(ctx, storage.ApprovalPending)
}

// Get returns a single approval by id.
func (g *Gate) Get(ctx context.Context, id string) (storage.ApprovalRecord, error) {
	return g.store.GetApproval(ctx, id)
}
