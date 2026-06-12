// Package audit builds an auditor-ready compliance export for a governed run. It
// maps the controls RiskKernel actually recorded — budgets, human approvals, tool
// governance, and the cost ledger — to the relevant OWASP and EU AI Act references,
// and emits a hash-chained, tamper-evident event log.
//
// Honesty note (a deliberate product constraint): this is an EVIDENCE export, not a
// legal compliance determination. It reports what RiskKernel deterministically
// enforced and recorded and which framework control each piece of evidence supports;
// an auditor evaluates sufficiency. Nothing here is inferred by an LLM.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/prashar32/riskkernel/internal/storage"
)

// Disclaimer is included verbatim in every report so the framing can't be lost.
const Disclaimer = "Evidence export of RiskKernel's recorded deterministic controls mapped to the referenced frameworks. Not a legal compliance determination; an auditor evaluates sufficiency."

// RunData is everything a compliance report is built from — all read from the
// durable store, nothing recomputed.
type RunData struct {
	Run       storage.RunRecord
	Ledger    []storage.LedgerEntry
	Totals    storage.LedgerTotals
	ToolCalls []storage.ToolCallRecord
	Approvals []storage.ApprovalRecord
}

// Report is the compliance export.
type Report struct {
	Report      string    `json:"report"`
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generatedAt"`
	Disclaimer  string    `json:"disclaimer"`
	Run         RunView   `json:"run"`
	Controls    []Control `json:"controls"`
	Events      []Event   `json:"events"`
	Integrity   Integrity `json:"integrity"`
}

// RunView is the run's governance summary.
type RunView struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	HaltReason string         `json:"haltReason,omitempty"`
	Budget     map[string]any `json:"budget"`
	Usage      map[string]any `json:"usage"`
	CreatedAt  time.Time      `json:"createdAt"`
}

// Control is one governance control, the framework references it supports, and the
// evidence RiskKernel recorded for it.
type Control struct {
	Control   string         `json:"control"`
	Statement string         `json:"statement"`
	OWASP     []string       `json:"owasp"`
	EUAIAct   []string       `json:"euAiAct"`
	Evidence  map[string]any `json:"evidence"`
}

// Event is one entry in the append-only, hash-chained log. Hash =
// sha256(prevHash + canonical(seq,type,at,detail)); any reorder/edit breaks it.
type Event struct {
	Seq    int            `json:"seq"`
	Type   string         `json:"type"`
	At     time.Time      `json:"at"`
	Detail map[string]any `json:"detail"`
	Hash   string         `json:"hash"`
}

// Integrity lets an auditor re-derive and verify the chain.
type Integrity struct {
	Algo        string `json:"algo"`
	Events      int    `json:"events"`
	ChainHead   string `json:"chainHead"`
	HowToVerify string `json:"howToVerify"`
}

// BuildReport assembles the compliance report from a run's recorded data.
func BuildReport(d RunData, now time.Time) Report {
	r := d.Run
	usage := r.UsagePromptTokens + r.UsageCompletionTokens
	rv := RunView{
		ID: r.ID, Name: r.Name, Status: r.Status, HaltReason: r.HaltReason,
		Budget:    map[string]any{"tokens": r.BudgetTokens, "dollars": r.BudgetDollars, "loops": r.BudgetLoops, "seconds": r.BudgetSeconds},
		Usage:     map[string]any{"tokens": usage, "dollars": r.UsageDollars, "loops": r.UsageLoops, "calls": d.Totals.Calls},
		CreatedAt: r.CreatedAt,
	}

	events := buildEvents(d)
	chainHead := ""
	if len(events) > 0 {
		chainHead = events[len(events)-1].Hash
	}

	return Report{
		Report:      "riskkernel-compliance",
		Version:     1,
		GeneratedAt: now,
		Disclaimer:  Disclaimer,
		Run:         rv,
		Controls:    buildControls(d),
		Events:      events,
		Integrity: Integrity{
			Algo: "sha256", Events: len(events), ChainHead: chainHead,
			HowToVerify: "For each event in order: hash = sha256(prevHash + canonicalJSON({seq,type,at,detail})), hex; prevHash starts empty. The last hash must equal chainHead.",
		},
	}
}

// buildControls maps recorded evidence to framework references. References are
// RiskKernel's mapping of which control each piece of evidence supports.
func buildControls(d RunData) []Control {
	r := d.Run
	halted := r.Status == "halted"

	gated := 0
	blocked := 0
	for _, t := range d.ToolCalls {
		switch t.Status {
		case "blocked":
			blocked++
		case "approved", "denied":
			gated++
		}
	}
	resolved := make([]map[string]any, 0, len(d.Approvals))
	for _, a := range d.Approvals {
		resolved = append(resolved, map[string]any{
			"tool": a.Tool, "sideEffect": a.SideEffect, "status": a.Status,
			"decidedBy": a.DecidedBy, "createdAt": a.CreatedAt,
		})
	}

	return []Control{
		{
			Control:   "budget_enforcement",
			Statement: "Hard per-run cost, token, loop, and time ceilings enforced deterministically; the run is halted on breach.",
			OWASP:     []string{"OWASP LLM Top 10 (2025) — LLM10: Unbounded Consumption", "OWASP Agentic Threats — Resource Overload"},
			EUAIAct:   []string{"Art. 15 — Accuracy, robustness and cybersecurity"},
			Evidence: map[string]any{
				"budget": map[string]any{"tokens": r.BudgetTokens, "dollars": r.BudgetDollars, "loops": r.BudgetLoops, "seconds": r.BudgetSeconds},
				"usage":  map[string]any{"tokens": r.UsagePromptTokens + r.UsageCompletionTokens, "dollars": r.UsageDollars, "loops": r.UsageLoops},
				"halted": halted, "haltReason": r.HaltReason,
			},
		},
		{
			Control:   "human_oversight",
			Statement: "Side-effecting actions can be gated on human approval; every decision records who decided, what, and when.",
			OWASP:     []string{"OWASP LLM Top 10 (2025) — LLM08: Excessive Agency", "OWASP Agentic Threats — Insufficient human-in-the-loop"},
			EUAIAct:   []string{"Art. 14 — Human oversight"},
			Evidence:  map[string]any{"approvals": resolved, "count": len(d.Approvals)},
		},
		{
			Control:   "tool_governance",
			Statement: "Tool calls are governed against an allowlist and recorded; disallowed calls are blocked.",
			OWASP:     []string{"OWASP LLM Top 10 (2025) — LLM08: Excessive Agency", "OWASP Agentic Threats — Tool Misuse"},
			EUAIAct:   []string{"Art. 15 — Accuracy, robustness and cybersecurity"},
			Evidence:  map[string]any{"toolCalls": len(d.ToolCalls), "gated": gated, "blocked": blocked},
		},
		{
			Control:   "record_keeping",
			Statement: "An append-only cost ledger and a hash-chained event log provide a tamper-evident record of the run.",
			OWASP:     []string{"OWASP Agentic Threats — Repudiation"},
			EUAIAct:   []string{"Art. 12 — Record-keeping (automatic logging of events)"},
			Evidence:  map[string]any{"ledgerEntries": len(d.Ledger), "toolCalls": len(d.ToolCalls), "approvals": len(d.Approvals)},
		},
	}
}

// buildEvents flattens the run's records into one time-ordered, hash-chained log.
func buildEvents(d RunData) []Event {
	type raw struct {
		at     time.Time
		typ    string
		detail map[string]any
	}
	var rs []raw
	for _, e := range d.Ledger {
		rs = append(rs, raw{e.CreatedAt, "model_call", map[string]any{
			"step": e.StepIndex, "provider": e.Provider, "model": e.Model,
			"promptTokens": e.PromptTokens, "completionTokens": e.CompletionTokens, "dollars": e.Dollars,
		}})
	}
	for _, t := range d.ToolCalls {
		rs = append(rs, raw{t.CreatedAt, "tool_call", map[string]any{
			"step": t.StepIndex, "tool": t.Tool, "sideEffect": t.SideEffect, "status": t.Status,
		}})
	}
	for _, a := range d.Approvals {
		at := a.CreatedAt
		if a.DecidedAt != nil {
			at = *a.DecidedAt
		}
		rs = append(rs, raw{at, "approval", map[string]any{
			"tool": a.Tool, "sideEffect": a.SideEffect, "status": a.Status, "decidedBy": a.DecidedBy,
		}})
	}
	// Stable order: by time, then type, so the chain is deterministic.
	sort.SliceStable(rs, func(i, j int) bool {
		if !rs[i].at.Equal(rs[j].at) {
			return rs[i].at.Before(rs[j].at)
		}
		return rs[i].typ < rs[j].typ
	})

	events := make([]Event, 0, len(rs))
	prev := ""
	for i, r := range rs {
		ev := Event{Seq: i, Type: r.typ, At: r.at, Detail: r.detail}
		ev.Hash = chainHash(prev, ev)
		prev = ev.Hash
		events = append(events, ev)
	}
	return events
}

// chainHash = sha256(prevHash + canonicalJSON({seq,type,at,detail})), hex.
func chainHash(prev string, ev Event) string {
	canonical, _ := json.Marshal(struct {
		Seq    int            `json:"seq"`
		Type   string         `json:"type"`
		At     time.Time      `json:"at"`
		Detail map[string]any `json:"detail"`
	}{ev.Seq, ev.Type, ev.At, ev.Detail})
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyChain re-derives the event chain and reports whether it matches each
// event's stored hash and the given head — the auditor-side check.
func VerifyChain(events []Event, head string) error {
	prev := ""
	for i, ev := range events {
		want := chainHash(prev, Event{Seq: ev.Seq, Type: ev.Type, At: ev.At, Detail: ev.Detail})
		if want != ev.Hash {
			return fmt.Errorf("event %d hash mismatch: chain broken (record altered or reordered)", i)
		}
		prev = ev.Hash
	}
	if prev != head {
		return fmt.Errorf("chain head mismatch: events truncated or appended")
	}
	return nil
}
