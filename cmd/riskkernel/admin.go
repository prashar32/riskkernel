package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/prashar32/riskkernel/internal/app"
	"github.com/prashar32/riskkernel/internal/config"
	"github.com/prashar32/riskkernel/internal/storage"
)

// runRuns implements `riskkernel runs list` — read-only inspection of persisted
// runs from the local state store.
func runRuns(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("usage: riskkernel runs list")
	}
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

// runAudit implements `riskkernel audit export <run-id>` — emit the auditable
// cost ledger for a run as JSON. Money must be auditable (CLAUDE.md §10).
func runAudit(args []string) error {
	if len(args) < 2 || args[0] != "export" {
		return fmt.Errorf("usage: riskkernel audit export <run-id>")
	}
	runID := args[1]

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

	out := struct {
		RunID   string                `json:"run_id"`
		Totals  storage.LedgerTotals  `json:"totals"`
		Entries []storage.LedgerEntry `json:"entries"`
	}{RunID: runID, Totals: totals, Entries: ledger}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
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
