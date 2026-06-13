package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

const sampleYAML = `
schemaVersion: 1
policies:
  - name: developer
    budget: { tokens: 200000, dollars: 5.00, loops: 50, seconds: 1800 }
    toolAllowlist: [ "mcp://github", "mcp://filesystem" ]
    approvalPolicy:
      requireFor:
        - { tool: "mcp://shell" }
        - { sideEffect: "*write*" }
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "riskkernel.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Valid(t *testing.T) {
	f, err := Load(writeTemp(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Policies) != 1 {
		t.Fatalf("policies = %d", len(f.Policies))
	}
	b := f.Policies[0]
	if b.Name != "developer" || b.Budget.Dollars != 5 || b.Budget.Loops != 50 {
		t.Fatalf("bundle = %+v", b)
	}
	if len(b.ToolAllowlist) != 2 || len(b.ApprovalPolicy.RequireFor) != 2 {
		t.Fatalf("allowlist/rules = %+v", b)
	}
	// Record() mirrors the storage shape used by POST /v1/policies.
	rec := b.Record(time.Unix(0, 0))
	if rec.BudgetLoops != 50 || len(rec.ApprovalRules) != 2 || rec.ToolAllowlist[0] != "mcp://github" {
		t.Fatalf("record = %+v", rec)
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	_, err := Load(writeTemp(t, "schemaVersion: 1\npolicies:\n  - name: x\n    budgett: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected a parse error on an unknown field, got %v", err)
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := map[string]string{
		"bad schema":      "schemaVersion: 2\npolicies:\n  - name: x\n",
		"no policies":     "schemaVersion: 1\npolicies: []\n",
		"no name":         "schemaVersion: 1\npolicies:\n  - budget: { loops: 1 }\n",
		"duplicate name":  "schemaVersion: 1\npolicies:\n  - name: a\n  - name: a\n",
		"negative budget": "schemaVersion: 1\npolicies:\n  - name: a\n    budget: { dollars: -1 }\n",
	}
	for name, yaml := range cases {
		if _, err := Load(writeTemp(t, yaml)); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}

func TestDryRun(t *testing.T) {
	f, err := Load(writeTemp(t, sampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	b := f.Policies[0]

	d := RunData{
		Run:    storage.RunRecord{ID: "run-1", UsageLoops: 60}, // over the 50-loop budget
		Totals: storage.LedgerTotals{Dollars: 6.0, PromptTokens: 1000, CompletionTokens: 500},
		ToolCalls: []storage.ToolCallRecord{
			{StepIndex: 1, Tool: "mcp://github", SideEffect: ""},          // allowed, no approval
			{StepIndex: 2, Tool: "mcp://shell", SideEffect: "exec"},       // not allowlisted + needs approval
			{StepIndex: 3, Tool: "mcp://filesystem", SideEffect: "write"}, // allowed but needs approval (*write*)
		},
	}
	rep := b.DryRun(d)

	// $6 ≥ $5 budget trips first (dollars checked before loops).
	if rep.BudgetHalt != "dollar_budget_exceeded" {
		t.Fatalf("budget halt = %q, want dollar_budget_exceeded", rep.BudgetHalt)
	}
	// mcp://shell is the only tool not in the allowlist.
	if len(rep.BlockedCalls) != 1 || rep.BlockedCalls[0].Tool != "mcp://shell" {
		t.Fatalf("blocked = %+v", rep.BlockedCalls)
	}
	// shell (tool rule) and filesystem write (*write* rule) need approval.
	if len(rep.GatedCalls) != 2 {
		t.Fatalf("gated = %+v", rep.GatedCalls)
	}
	if s := rep.String(); !strings.Contains(s, "WOULD HALT") || !strings.Contains(s, "mcp://shell") {
		t.Fatalf("report string missing detail:\n%s", s)
	}
}

func TestDryRun_WithinBudget(t *testing.T) {
	f, _ := Load(writeTemp(t, sampleYAML))
	rep := f.Policies[0].DryRun(RunData{
		Run:    storage.RunRecord{ID: "r", UsageLoops: 10},
		Totals: storage.LedgerTotals{Dollars: 1.0, PromptTokens: 100},
	})
	if rep.BudgetHalt != "" {
		t.Fatalf("expected within budget, got halt %q", rep.BudgetHalt)
	}
}
