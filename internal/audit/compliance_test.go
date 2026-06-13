package audit

import (
	"testing"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

func sampleData() RunData {
	base := time.Unix(1_700_000_000, 0).UTC()
	dec := base.Add(30 * time.Second)
	return RunData{
		Run: storage.RunRecord{
			ID: "run-1", Name: "demo", Status: "halted", HaltReason: "dollar_budget_exceeded",
			BudgetDollars: 5, BudgetLoops: 50,
			UsageDollars: 6, UsageLoops: 12, UsagePromptTokens: 1000, UsageCompletionTokens: 500,
			CreatedAt: base,
		},
		Totals: storage.LedgerTotals{Calls: 2, Dollars: 6, PromptTokens: 1000, CompletionTokens: 500},
		Ledger: []storage.LedgerEntry{
			{StepIndex: 1, Provider: "anthropic", Model: "m", Dollars: 3, CreatedAt: base.Add(1 * time.Second)},
			{StepIndex: 2, Provider: "anthropic", Model: "m", Dollars: 3, CreatedAt: base.Add(2 * time.Second)},
		},
		ToolCalls: []storage.ToolCallRecord{
			{StepIndex: 2, Tool: "mcp://shell", SideEffect: "exec", Status: "approved", CreatedAt: base.Add(3 * time.Second)},
			{StepIndex: 2, Tool: "mcp://email", SideEffect: "send", Status: "blocked", CreatedAt: base.Add(4 * time.Second)},
		},
		Approvals: []storage.ApprovalRecord{
			{ID: "a1", RunID: "run-1", Tool: "mcp://shell", SideEffect: "exec", Status: "approved", DecidedBy: "slack:alice", CreatedAt: base.Add(2 * time.Second), DecidedAt: &dec},
		},
	}
}

func TestBuildReport(t *testing.T) {
	rep := BuildReport(sampleData(), time.Unix(1_700_001_000, 0).UTC())

	if rep.Report != "riskkernel-compliance" || rep.Disclaimer == "" {
		t.Fatalf("report header = %+v", rep)
	}
	if rep.Run.ID != "run-1" || rep.Run.Status != "halted" {
		t.Fatalf("run view = %+v", rep.Run)
	}
	// One control per recorded dimension, each with framework references.
	wantControls := map[string]bool{"budget_enforcement": false, "human_oversight": false, "tool_governance": false, "record_keeping": false}
	for _, c := range rep.Controls {
		if _, ok := wantControls[c.Control]; !ok {
			t.Fatalf("unexpected control %q", c.Control)
		}
		wantControls[c.Control] = true
		if len(c.OWASP) == 0 || len(c.EUAIAct) == 0 {
			t.Fatalf("control %q missing framework refs", c.Control)
		}
	}
	for name, seen := range wantControls {
		if !seen {
			t.Fatalf("missing control %q", name)
		}
	}
	// 2 model calls + 2 tool calls + 1 approval = 5 events, chained.
	if len(rep.Events) != 5 || rep.Integrity.Events != 5 || rep.Integrity.ChainHead == "" {
		t.Fatalf("events = %d, integrity = %+v", len(rep.Events), rep.Integrity)
	}
	if err := VerifyChain(rep.Events, rep.Integrity.ChainHead); err != nil {
		t.Fatalf("a freshly built report must verify: %v", err)
	}
}

func TestBuildReport_Deterministic(t *testing.T) {
	a := BuildReport(sampleData(), time.Unix(1, 0))
	b := BuildReport(sampleData(), time.Unix(2, 0)) // different generatedAt
	// The event chain must not depend on when the report was generated.
	if a.Integrity.ChainHead != b.Integrity.ChainHead {
		t.Fatalf("chain head not deterministic: %s != %s", a.Integrity.ChainHead, b.Integrity.ChainHead)
	}
}

func TestVerifyChain_DetectsTampering(t *testing.T) {
	rep := BuildReport(sampleData(), time.Unix(1, 0))
	head := rep.Integrity.ChainHead

	// Altering an event's detail breaks the chain.
	tampered := make([]Event, len(rep.Events))
	copy(tampered, rep.Events)
	tampered[1].Detail = map[string]any{"dollars": 999}
	if err := VerifyChain(tampered, head); err == nil {
		t.Fatal("altered event detail must fail verification")
	}

	// Dropping the last event breaks the head match.
	if err := VerifyChain(rep.Events[:len(rep.Events)-1], head); err == nil {
		t.Fatal("truncated events must fail head verification")
	}

	// Swapping a stored hash breaks the chain.
	tampered2 := make([]Event, len(rep.Events))
	copy(tampered2, rep.Events)
	tampered2[0].Hash = "deadbeef"
	if err := VerifyChain(tampered2, head); err == nil {
		t.Fatal("a forged event hash must fail verification")
	}
}
