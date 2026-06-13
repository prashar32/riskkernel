// Package storage is RiskKernel's durable state layer. The Store interface is the
// seam behind which SQLite (default, zero-config, the file the user owns) and
// Postgres (opt-in, later) live. Callers never see SQL. Runs, steps, tool calls,
// and the cost ledger persist here so runs survive a crash (step 5: resume) and
// so spend is auditable (`riskkernel audit export`).
//
// Schema evolution is forward-only via embedded Goose migrations (see
// COMPATIBILITY.md): migrations run in a transaction on startup, and the daemon
// refuses to start if the on-disk schema is newer than the binary understands.
package storage

import (
	"context"
	"time"
)

// RunRecord is the persisted form of a governed run.
type RunRecord struct {
	ID         string
	Name       string
	Status     string
	HaltReason string
	// PolicyRef is the name of the policy bundle the run was created under (empty
	// = none). Its tool allowlist and approval rules are enforced per-run.
	PolicyRef string

	BudgetTokens  int64
	BudgetDollars float64
	BudgetLoops   int32
	BudgetSeconds int32

	UsagePromptTokens     int64
	UsageCompletionTokens int64
	UsageDollars          float64
	UsageLoops            int32

	Metadata  map[string]string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// StepRecord is one loop iteration of a run.
type StepRecord struct {
	RunID            string
	Index            int32
	Status           string
	PromptTokens     int64
	CompletionTokens int64
	Dollars          float64
	StartedAt        time.Time
	EndedAt          *time.Time // nil while running
}

// LedgerEntry is one priced model call — the auditable unit of spend.
type LedgerEntry struct {
	RunID            string
	StepIndex        int32
	Provider         string
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	Dollars          float64
	Priced           bool // false when the model had no known price
	ResponseID       string
	CreatedAt        time.Time
}

// ToolCallRecord is a (side-effecting) tool invocation; populated by the MCP
// gateway and HITL approval gate in later build steps.
type ToolCallRecord struct {
	ID         string
	RunID      string
	StepIndex  int32
	Tool       string
	SideEffect string
	Arguments  map[string]any
	Status     string
	CreatedAt  time.Time
}

// CheckpointRecord is a crash-resumable snapshot: a run's usage at a step plus an
// opaque user-supplied payload to restart from.
type CheckpointRecord struct {
	RunID                 string
	StepIndex             int32
	Name                  string
	UsagePromptTokens     int64
	UsageCompletionTokens int64
	UsageDollars          float64
	UsageLoops            int32
	Payload               map[string]any
	CreatedAt             time.Time
}

// Approval status values.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
)

// ApprovalRecord is a human-in-the-loop gate on a side-effecting tool call.
type ApprovalRecord struct {
	ID         string
	RunID      string
	StepIndex  int32
	Tool       string
	SideEffect string
	Arguments  map[string]any
	Status     string // pending | approved | denied
	Reason     string
	DecidedBy  string
	CreatedAt  time.Time
	DecidedAt  *time.Time
}

// ApprovalRule mirrors an api/v1 ApprovalPolicy rule: an action needs approval if
// it matches the tool exactly or the side-effect glob.
type ApprovalRule struct {
	Tool       string `json:"tool,omitempty"`
	SideEffect string `json:"sideEffect,omitempty"`
}

// PolicyRecord is a reusable, named policy bundle — a default budget, a tool
// allowlist, and approval rules — that a run can reference by name (policyRef)
// instead of inlining. Mirrors the api/v1 Policy schema.
type PolicyRecord struct {
	Name          string
	BudgetTokens  int64
	BudgetDollars float64
	BudgetLoops   int32
	BudgetSeconds int32
	ToolAllowlist []string
	ApprovalRules []ApprovalRule
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// LedgerTotals aggregates spend for audit/reporting.
type LedgerTotals struct {
	RunID            string  `json:"runId"`
	Calls            int64   `json:"calls"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	Dollars          float64 `json:"dollars"`
}

// UsageGroup is one bucket of aggregated spend — e.g. one team, one provider, or
// one day. Tokens/dollars are summed from the cost ledger (the auditable source).
type UsageGroup struct {
	Key              string  `json:"key"`
	Calls            int64   `json:"calls"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	Dollars          float64 `json:"dollars"`
}

// UsageSummary is cost-ledger spend grouped by one dimension, plus the grand total
// across all groups in range.
type UsageSummary struct {
	By     string       `json:"by"`
	Groups []UsageGroup `json:"groups"`
	Total  UsageGroup   `json:"total"`
}

// SummarizeOptions selects how SummarizeLedger aggregates the cost ledger.
type SummarizeOptions struct {
	// By is the grouping dimension: "provider", "model", "day", "name", or
	// "metadata.<key>" (e.g. "metadata.team"). Required.
	By string
	// Since/Until optionally bound the call time (created_at): Since is inclusive,
	// Until exclusive. Nil means unbounded on that end.
	Since *time.Time
	Until *time.Time
}

// Store is the durable backend. Implementations must be safe for concurrent use.
type Store interface {
	// UpsertRun inserts or replaces a run row by ID.
	UpsertRun(ctx context.Context, r RunRecord) error
	// GetRun returns a run by ID, or ErrNotFound.
	GetRun(ctx context.Context, id string) (RunRecord, error)
	// ListRuns returns all runs, newest first.
	ListRuns(ctx context.Context) ([]RunRecord, error)
	// ListRunsByStatus returns runs in the given lifecycle status (e.g. "running"
	// for reload-on-startup), newest first.
	ListRunsByStatus(ctx context.Context, status string) ([]RunRecord, error)

	// UpsertStep inserts or replaces a step row by (run_id, index).
	UpsertStep(ctx context.Context, s StepRecord) error
	// ListSteps returns a run's steps in index order.
	ListSteps(ctx context.Context, runID string) ([]StepRecord, error)

	// AppendLedger appends one priced call to the cost ledger.
	AppendLedger(ctx context.Context, e LedgerEntry) error
	// LedgerForRun returns a run's ledger entries in time order.
	LedgerForRun(ctx context.Context, runID string) ([]LedgerEntry, error)
	// Totals aggregates a run's ledger.
	Totals(ctx context.Context, runID string) (LedgerTotals, error)
	// SummarizeLedger aggregates spend across runs, grouped by opts.By
	// (provider/model/day/name/metadata.<key>). The unit is the ledger row.
	SummarizeLedger(ctx context.Context, opts SummarizeOptions) (UsageSummary, error)

	// AppendToolCall records a tool invocation.
	AppendToolCall(ctx context.Context, t ToolCallRecord) error
	// ListToolCalls returns a run's tool invocations in audit order.
	ListToolCalls(ctx context.Context, runID string) ([]ToolCallRecord, error)

	// PutFact inserts or updates an episodic memory fact by (namespace, key).
	PutFact(ctx context.Context, f Fact) error
	// GetFact returns a fact, or ErrNotFound.
	GetFact(ctx context.Context, namespace, key string) (Fact, error)
	// ListFacts returns all facts in a namespace.
	ListFacts(ctx context.Context, namespace string) ([]Fact, error)

	// CreateApproval persists a new (pending) approval request.
	CreateApproval(ctx context.Context, a ApprovalRecord) error
	// GetApproval returns an approval by id, or ErrNotFound.
	GetApproval(ctx context.Context, id string) (ApprovalRecord, error)
	// ResolveApproval records a decision (approved/denied) on a pending approval.
	ResolveApproval(ctx context.Context, id, status, reason, decidedBy string, decidedAt time.Time) error
	// ListApprovals returns approvals filtered by status ("" = all), newest first.
	ListApprovals(ctx context.Context, status string) ([]ApprovalRecord, error)

	// UpsertPolicy inserts or replaces a named policy bundle (register/update by name).
	UpsertPolicy(ctx context.Context, p PolicyRecord) error
	// GetPolicy returns a policy bundle by name, or ErrNotFound.
	GetPolicy(ctx context.Context, name string) (PolicyRecord, error)
	// ListPolicies returns all policy bundles, newest first.
	ListPolicies(ctx context.Context) ([]PolicyRecord, error)

	// SaveCheckpoint appends a crash-resumable checkpoint.
	SaveCheckpoint(ctx context.Context, c CheckpointRecord) error
	// LatestCheckpoint returns a run's most recent checkpoint, or ErrNotFound.
	LatestCheckpoint(ctx context.Context, runID string) (CheckpointRecord, error)
	// ListCheckpoints returns a run's checkpoints in time order.
	ListCheckpoints(ctx context.Context, runID string) ([]CheckpointRecord, error)

	// Close releases the backend's resources.
	Close() error
}
