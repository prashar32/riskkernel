# Compliance evidence export

```bash
riskkernel audit compliance <run-id> > run-evidence.json
```

Produces an **auditor-ready evidence export** for a governed run: the controls
RiskKernel actually enforced and recorded — budgets, human approvals, tool
governance, the cost ledger — mapped to the relevant **OWASP** and **EU AI Act**
references, plus a **hash-chained, tamper-evident** event log.

> **What this is — and isn't.** This is an *evidence* export: it reports what
> RiskKernel deterministically enforced and recorded, and which framework control
> each piece of evidence supports. It is **not a legal compliance determination** —
> an auditor evaluates sufficiency. The same disclaimer is embedded in every report.
> Nothing in it is inferred by an LLM. (This honesty is a product constraint, not a
> hedge: RiskKernel only claims what it can show from the record.)

## Control mapping

| Control | Evidence RiskKernel recorded | EU AI Act | OWASP |
|---|---|---|---|
| **Budget enforcement** | budget, usage, halt + reason | Art. 15 — robustness | LLM10 Unbounded Consumption · Agentic: Resource Overload |
| **Human oversight** | every approval (who / what / when / decision) | Art. 14 — human oversight | LLM08 Excessive Agency · Agentic: Insufficient HITL |
| **Tool governance** | tool calls, allowed vs blocked | Art. 15 — cybersecurity | LLM08 Excessive Agency · Agentic: Tool Misuse |
| **Record-keeping** | cost ledger + hash-chained event log | Art. 12 — record-keeping | Agentic: Repudiation |

## Tamper-evidence

The export flattens the run's model calls, tool calls, and approvals into one
time-ordered event log, each event carrying a hash:

```
hash = sha256( prevHash + canonicalJSON({seq, type, at, detail}) )   # hex; prevHash starts empty
```

The `integrity.chainHead` is the last event's hash. **Any** edit, reorder, or
truncation of the events changes a hash and breaks the chain, so an auditor can
re-derive it and detect tampering — the procedure is embedded in `integrity.howToVerify`.
The chain is independent of when the report was generated, so it's reproducible.

## Sample

```json
{
  "report": "riskkernel-compliance",
  "version": 1,
  "generatedAt": "2026-06-13T12:00:00Z",
  "disclaimer": "Evidence export of RiskKernel's recorded deterministic controls mapped to the referenced frameworks. Not a legal compliance determination; an auditor evaluates sufficiency.",
  "run": {
    "id": "4f3c…", "name": "nightly-research", "status": "halted",
    "haltReason": "dollar_budget_exceeded",
    "budget": { "dollars": 5, "loops": 50, "tokens": 0, "seconds": 1800 },
    "usage":  { "dollars": 6.42, "loops": 12, "tokens": 184213, "calls": 12 }
  },
  "controls": [
    {
      "control": "human_oversight",
      "statement": "Side-effecting actions can be gated on human approval; every decision records who decided, what, and when.",
      "owasp": ["OWASP LLM Top 10 (2025) — LLM08: Excessive Agency", "OWASP Agentic Threats — Insufficient human-in-the-loop"],
      "euAiAct": ["Art. 14 — Human oversight"],
      "evidence": { "count": 1, "approvals": [
        { "tool": "mcp://shell", "sideEffect": "exec", "status": "approved", "decidedBy": "slack:alice" }
      ] }
    }
    // … budget_enforcement, tool_governance, record_keeping …
  ],
  "events": [
    { "seq": 0, "type": "model_call", "at": "…", "detail": { "step": 1, "dollars": 0.53 }, "hash": "a1b2…" },
    { "seq": 1, "type": "tool_call",  "at": "…", "detail": { "tool": "mcp://shell", "status": "approved" }, "hash": "c3d4…" }
  ],
  "integrity": {
    "algo": "sha256", "events": 14, "chainHead": "c3d4…",
    "howToVerify": "For each event in order: hash = sha256(prevHash + canonicalJSON({seq,type,at,detail})), hex; prevHash starts empty. The last hash must equal chainHead."
  }
}
```

The approval `decidedBy` carries the channel and identity (e.g. `slack:alice`) from
the [Slack approval channel](APPROVALS_SLACK.md) or whichever channel resolved it.
