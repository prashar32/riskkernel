package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/prashar32/riskkernel/internal/app"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/storage"
)

// runRuns implements `riskkernel runs <list|resume>`.
func runRuns(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: riskkernel runs <list|resume <id>>")
	}
	switch args[0] {
	case "list":
		return runsList()
	case "resume":
		if len(args) < 2 {
			return fmt.Errorf("usage: riskkernel runs resume <id>")
		}
		return runsResume(args[1])
	default:
		return fmt.Errorf("unknown runs subcommand %q (want list|resume)", args[0])
	}
}

// runsList prints persisted runs from the local state store.
func runsList() error {
	store, err := openStoreForCLI()
	if err != nil {
		return err
	}
	defer store.Close()

	rs, err := store.ListRuns(context.Background())
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		fmt.Println("no runs yet")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tTOKENS\tDOLLARS\tLOOPS\tHALT\tCREATED")
	for _, r := range rs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%.4f\t%d\t%s\t%s\n",
			r.ID, dash(r.Name), r.Status,
			r.UsagePromptTokens+r.UsageCompletionTokens, r.UsageDollars, r.UsageLoops,
			dash(r.HaltReason), r.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	return tw.Flush()
}

// runsResume implements `riskkernel runs resume <id>` — report a run's resumable
// state. A running daemon auto-reloads non-terminal runs on startup, so a
// SIGKILL'd run keeps enforcing against its spent budget as soon as the daemon is
// back; this command confirms what state it will continue from. Terminal runs are
// not resumable.
func runsResume(runID string) error {
	store, err := openStoreForCLI()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("run %s: %w", runID, err)
	}

	switch run.Status {
	case "completed", "halted", "cancelled", "failed":
		fmt.Printf("run %s is %s and cannot be resumed (halt: %s)\n", runID, run.Status, dash(run.HaltReason))
		return nil
	}

	usedTokens := run.UsagePromptTokens + run.UsageCompletionTokens
	fmt.Printf("run %s (%s) is resumable\n", runID, dash(run.Name))
	fmt.Printf("  spent so far : %d tokens, $%.4f, %d loops\n", usedTokens, run.UsageDollars, run.UsageLoops)
	fmt.Printf("  budget       : %s\n", budgetSummary(run))
	fmt.Printf("  remaining    : %s\n", remainingSummary(run, usedTokens))

	if cp, err := store.LatestCheckpoint(ctx, runID); err == nil {
		fmt.Printf("  last step    : %d (checkpoint at %s)\n", cp.StepIndex, cp.CreatedAt.Format("2006-01-02 15:04:05"))
		if len(cp.Payload) > 0 {
			b, _ := json.Marshal(cp.Payload)
			fmt.Printf("  payload      : %s\n", string(b))
		}
	}
	fmt.Println("\nStart the daemon (riskkernel serve) and reuse this run id; enforcement continues from the state above.")
	return nil
}

func budgetSummary(r storage.RunRecord) string {
	return fmt.Sprintf("%s tokens, %s, %s loops, %s",
		limitInt(r.BudgetTokens), limitDollars(r.BudgetDollars),
		limitInt(int64(r.BudgetLoops)), limitSeconds(r.BudgetSeconds))
}

func remainingSummary(r storage.RunRecord, usedTokens int64) string {
	parts := ""
	if r.BudgetTokens > 0 {
		parts += fmt.Sprintf("%d tokens", maxInt64(0, r.BudgetTokens-usedTokens))
	} else {
		parts += "unlimited tokens"
	}
	if r.BudgetDollars > 0 {
		parts += fmt.Sprintf(", $%.4f", maxFloat(0, r.BudgetDollars-r.UsageDollars))
	}
	if r.BudgetLoops > 0 {
		parts += fmt.Sprintf(", %d loops", maxInt64(0, int64(r.BudgetLoops)-int64(r.UsageLoops)))
	}
	return parts
}

func limitInt(v int64) string {
	if v <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", v)
}
func limitDollars(v float64) string {
	if v <= 0 {
		return "unlimited $"
	}
	return fmt.Sprintf("$%.2f", v)
}
func limitSeconds(v int32) string {
	if v <= 0 {
		return "unlimited time"
	}
	return fmt.Sprintf("%ds", v)
}
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// runAudit implements the local audit read-back commands.
func runAudit(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: riskkernel audit <export|tools> <run-id>")
	}
	switch args[0] {
	case "export":
		return auditExport(args[1])
	case "tools":
		return auditTools(args[1])
	default:
		return fmt.Errorf("unknown audit subcommand %q (want export|tools)", args[0])
	}
}

func auditExport(runID string) error {
	store, err := openStoreForCLI()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	if _, err := store.GetRun(ctx, runID); err != nil {
		return fmt.Errorf("run %s: %w", runID, err)
	}
	ledger, err := store.LedgerForRun(ctx, runID)
	if err != nil {
		return err
	}
	totals, err := store.Totals(ctx, runID)
	if err != nil {
		return err
	}
	toolCalls, err := store.ListToolCalls(ctx, runID)
	if err != nil {
		return err
	}

	out := struct {
		RunID     string                `json:"run_id"`
		Totals    storage.LedgerTotals  `json:"totals"`
		Entries   []storage.LedgerEntry `json:"entries"`
		ToolCalls []toolCallJSON        `json:"tool_calls"`
	}{RunID: runID, Totals: totals, Entries: ledger, ToolCalls: toolCallJSONSlice(toolCalls)}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func auditTools(runID string) error {
	store, err := openStoreForCLI()
	if err != nil {
		return err
	}
	defer store.Close()

	ctx := context.Background()
	if _, err := store.GetRun(ctx, runID); err != nil {
		return fmt.Errorf("run %s: %w", runID, err)
	}
	toolCalls, err := store.ListToolCalls(ctx, runID)
	if err != nil {
		return err
	}

	out := struct {
		RunID     string         `json:"run_id"`
		ToolCalls []toolCallJSON `json:"tool_calls"`
	}{RunID: runID, ToolCalls: toolCallJSONSlice(toolCalls)}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type toolCallJSON struct {
	ID         string         `json:"id"`
	RunID      string         `json:"runId"`
	StepIndex  int32          `json:"stepIndex"`
	Tool       string         `json:"tool"`
	SideEffect string         `json:"sideEffect"`
	Arguments  map[string]any `json:"arguments"`
	Status     string         `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
}

func toolCallJSONSlice(calls []storage.ToolCallRecord) []toolCallJSON {
	out := make([]toolCallJSON, 0, len(calls))
	for _, call := range calls {
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		out = append(out, toolCallJSON{
			ID:         call.ID,
			RunID:      call.RunID,
			StepIndex:  call.StepIndex,
			Tool:       call.Tool,
			SideEffect: call.SideEffect,
			Arguments:  args,
			Status:     call.Status,
			CreatedAt:  call.CreatedAt,
		})
	}
	return out
}

// openStoreForCLI opens the state store for a read-only CLI command.
func openStoreForCLI() (storage.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return app.OpenStore(cfg, app.NewLogger())
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
