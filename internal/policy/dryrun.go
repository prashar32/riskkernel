package policy

import (
	"fmt"
	"strings"

	"github.com/prashar32/riskkernel/internal/storage"
)

// RunData is the recorded state of a run that a dry-run evaluates a bundle against.
type RunData struct {
	Run       storage.RunRecord
	Totals    storage.LedgerTotals
	ToolCalls []storage.ToolCallRecord
}

// Report is the outcome of dry-running a bundle against a recorded run — what the
// bundle WOULD have done, changing nothing.
type Report struct {
	Policy       string
	RunID        string
	BudgetHalt   string // "" if within budget, else the dimension that would have halted
	BudgetDetail string
	GatedCalls   []storage.ToolCallRecord // would have required human approval
	BlockedCalls []storage.ToolCallRecord // not in the tool allowlist
	TotalCalls   int
}

// DryRun evaluates the bundle against a recorded run. It is read-only.
func (b Bundle) DryRun(d RunData) Report {
	rep := Report{Policy: b.Name, RunID: d.Run.ID, TotalCalls: len(d.ToolCalls)}

	// Budget: would the bundle's limits have halted this run? Report the first
	// dimension that would trip (the governor halts on the first breach).
	tokens := d.Totals.PromptTokens + d.Totals.CompletionTokens
	switch {
	case b.Budget.Dollars > 0 && d.Totals.Dollars >= b.Budget.Dollars:
		rep.BudgetHalt = "dollar_budget_exceeded"
		rep.BudgetDetail = fmt.Sprintf("$%.4f spent ≥ $%.2f budget", d.Totals.Dollars, b.Budget.Dollars)
	case b.Budget.Tokens > 0 && tokens >= b.Budget.Tokens:
		rep.BudgetHalt = "token_budget_exceeded"
		rep.BudgetDetail = fmt.Sprintf("%d tokens ≥ %d budget", tokens, b.Budget.Tokens)
	case b.Budget.Loops > 0 && d.Run.UsageLoops > b.Budget.Loops:
		rep.BudgetHalt = "loop_budget_exceeded"
		rep.BudgetDetail = fmt.Sprintf("%d loops > %d budget", d.Run.UsageLoops, b.Budget.Loops)
	}

	for _, c := range d.ToolCalls {
		if !b.allows(c.Tool) {
			rep.BlockedCalls = append(rep.BlockedCalls, c)
		}
		if b.requiresApproval(c.Tool, c.SideEffect) {
			rep.GatedCalls = append(rep.GatedCalls, c)
		}
	}
	return rep
}

// String renders a human-readable dry-run report.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Dry-run: policy %q vs run %s\n", r.Policy, r.RunID)

	if r.BudgetHalt != "" {
		fmt.Fprintf(&b, "  budget:    WOULD HALT — %s (%s)\n", r.BudgetHalt, r.BudgetDetail)
	} else {
		fmt.Fprintf(&b, "  budget:    within budget\n")
	}

	fmt.Fprintf(&b, "  allowlist: %d of %d tool calls blocked\n", len(r.BlockedCalls), r.TotalCalls)
	for _, c := range r.BlockedCalls {
		fmt.Fprintf(&b, "      ✗ step %d  %s\n", c.StepIndex, c.Tool)
	}
	fmt.Fprintf(&b, "  approval:  %d of %d tool calls would require sign-off\n", len(r.GatedCalls), r.TotalCalls)
	for _, c := range r.GatedCalls {
		se := c.SideEffect
		if se != "" {
			se = " (" + se + ")"
		}
		fmt.Fprintf(&b, "      ⏸ step %d  %s%s\n", c.StepIndex, c.Tool, se)
	}
	return b.String()
}
