package main

import (
	"context"
	"fmt"

	"github.com/prashar32/riskkernel/internal/policy"
)

// runPolicy implements `riskkernel policy <validate|dry-run>`.
func runPolicy(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: riskkernel policy <validate <file> | dry-run <file> <run-id> [policy-name]>")
	}
	switch args[0] {
	case "validate":
		return policyValidate(args[1:])
	case "dry-run", "dryrun":
		return policyDryRun(args[1:])
	default:
		return fmt.Errorf("unknown policy subcommand %q (want validate|dry-run)", args[0])
	}
}

// policyValidate parses a riskkernel.yaml and reports the bundles, or the error.
func policyValidate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: riskkernel policy validate <file>")
	}
	f, err := policy.Load(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("✓ %s — %d policy bundle(s), schemaVersion %d\n", args[0], len(f.Policies), f.SchemaVersion)
	for _, b := range f.Policies {
		fmt.Printf("  • %s  (budget: $%.2f / %d loops / %d tokens / %ds · allowlist: %d · approval rules: %d)\n",
			b.Name, b.Budget.Dollars, b.Budget.Loops, b.Budget.Tokens, b.Budget.Seconds,
			len(b.ToolAllowlist), len(b.ApprovalPolicy.RequireFor))
	}
	return nil
}

// policyDryRun replays a recorded run against a policy bundle and prints what the
// bundle WOULD have gated or halted — changing nothing.
func policyDryRun(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: riskkernel policy dry-run <file> <run-id> [policy-name]")
	}
	file, runID := args[0], args[1]

	f, err := policy.Load(file)
	if err != nil {
		return err
	}
	var bundle policy.Bundle
	if len(args) >= 3 {
		b, ok := f.Bundle(args[2])
		if !ok {
			return fmt.Errorf("no policy named %q in %s", args[2], file)
		}
		bundle = b
	} else if len(f.Policies) == 1 {
		bundle = f.Policies[0]
	} else {
		return fmt.Errorf("%s has %d policies; name one: riskkernel policy dry-run %s %s <policy-name>",
			file, len(f.Policies), file, runID)
	}

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
	totals, err := store.Totals(ctx, runID)
	if err != nil {
		return err
	}
	toolCalls, err := store.ListToolCalls(ctx, runID)
	if err != nil {
		return err
	}

	rep := bundle.DryRun(policy.RunData{Run: run, Totals: totals, ToolCalls: toolCalls})
	fmt.Print(rep.String())
	return nil
}
