# Cost benchmark — dollars saved on a runaway loop

A reproducible measurement of the headline claim: **RiskKernel caps the cost of a
runaway agent.** The same looping agent runs twice against a deterministic mock
provider — once with no governance, once through RiskKernel with a hard dollar
budget — and we compare the spend.

```
  RiskKernel cost benchmark — runaway loop
  ------------------------------------------------------
  loop length (N)            50
  dollar budget              $0.25
  per-call cost              $0.0125   (gpt-4o, from RiskKernel's ledger)
  ------------------------------------------------------
                            calls        spend
  baseline (no governance)     50      $0.6250
  governed (RiskKernel)        20      $0.2500
  ------------------------------------------------------
  dollars saved              $0.3750   (60%)
  stopped by                 dollar_budget_exceeded
```

## Run it

```bash
go install github.com/prashar32/riskkernel/cmd/riskkernel@latest   # or: make build
python3 benchmark/benchmark.py
```

No API key, no real spend. Tunable via env: `N` (loop length), `BUDGET` (dollar
ceiling), `RK_BIN` (path to the binary, default `riskkernel` on `PATH`).

## Methodology (why the number is honest)

The benchmark is built so the **only** difference between the two runs is whether
RiskKernel's budget stopped the loop:

- **Deterministic provider.** [`mock_provider.py`](mock_provider.py) is a stand-in
  for the OpenAI Chat Completions API that returns a *fixed* token usage on every
  call (1000 in / 1000 out). No network variance, no real money, exactly
  reproducible. RiskKernel reaches it via the namespaced
  `RISKKERNEL_OPENAI_BASE_URL` override — the agent never touches a real provider.
- **Real prices, pinned.** [`pricing.json`](pricing.json) pins `gpt-4o` at its list
  price ($2.50 / $10.00 per 1M input / output tokens). Per call =
  `1000·2.50/1e6 + 1000·10.00/1e6 = $0.0125`.
- **The governed spend is measured, not modelled.** It's read straight from
  RiskKernel's own cost ledger (`GET /v1/runs/{id}` → `usage.dollars`). RiskKernel
  meters each call and halts the run *pre-call* once the next call would exceed the
  budget — here at exactly `20 × $0.0125 = $0.25`.
- **The baseline uses that same per-call price** across the full loop, so the two
  numbers are directly comparable.

## What the number actually says

The governed run's spend is **capped at the budget regardless of how long the
runaway would have continued.** The baseline grows without bound:

| If the runaway loops… | Baseline spend | Governed spend | Saved |
|---|---|---|---|
| 50× | $0.63 | **$0.25** | $0.38 |
| 1,000× | $12.50 | **$0.25** | $12.25 |
| 10,000× | $125.00 | **$0.25** | $124.75 |

So "dollars saved" is a function of how far the loop would have run before a human
noticed — and RiskKernel's guarantee is the **flat ceiling**, not a fixed percentage.

## Scope & honesty

- This measures the **cost-ceiling** dimension. The **crash-recovery** dimension —
  `kill -9` mid-run, resume without re-spending — is demonstrated end-to-end in
  [`examples/kill-9-resume`](../examples/kill-9-resume); a *timed* recovery
  benchmark is a planned addition here.
- It deliberately removes provider latency/variance to isolate the governance
  effect. The enforcement overhead RiskKernel itself adds is small and measured
  separately; this harness is about dollars, not milliseconds.
- The mock and pricing are in this directory — inspect and change them. Nothing is
  hidden behind a wrapper.
