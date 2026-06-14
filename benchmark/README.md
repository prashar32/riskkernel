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

## Recovery time — `kill -9` mid-run, resume without re-spending

The second dimension. [`recovery.py`](recovery.py) measures **crash recovery**: a
governed, checkpointing run is interrupted by a hard `kill -9` of the daemon
mid-run, the daemon is restarted on the same durable data dir, and we time how long
until it's healthy with the run reloaded — then prove the run finishes **without
re-spending**.

```bash
python3 benchmark/recovery.py
```

```
  RiskKernel recovery benchmark — kill -9 mid-run, resume without re-spending
  ------------------------------------------------------------------
  dollar budget              $0.25
  per-call cost              $0.0125   (gpt-4o, from RiskKernel's ledger)
  calls before crash         8
  ------------------------------------------------------------------
  RECOVERY TIME              53 ms   (kill -9 -> healthy + run reloaded)
  daemon log                 "resumed runs from store" count=1
  ------------------------------------------------------------------
  spend (ledger)                 dollars   tokens  loops
  before crash                   $0.1000    16000      8
  after restart (reloaded)       $0.1000    16000      8
  final (budget halt)            $0.2500    40000     20
  ------------------------------------------------------------------
  meter NOT reset by crash   yes  (after-restart spend >= before)
  meter NOT double-counted   yes  (after-restart spend == before)
  halted at original budget  yes  (dollar_budget_exceeded)
  EXACT-ONCE across crash    PASS
```

Tunable via env: `BUDGET`, `PRE_CRASH_CALLS` (calls before the crash), `N` (safety
cap on the loop), `RK_BIN`.

### Methodology (the exact-once guarantee)

Same key-free, deterministic setup as the cost benchmark — the mock provider, a
dummy key, spend read from RiskKernel's own ledger. The sequence:

1. **Spend, then checkpoint.** The run makes several governed calls through
   RiskKernel (grouped by a run-id header) and checkpoints between them. The spend so
   far is read from the ledger (`GET /v1/runs/{id}` → `usage.dollars / tokens /
   loops`) — *before crash* in the table.
2. **Hard crash.** The daemon is `SIGKILL`'d — no graceful shutdown, no chance to
   flush. Everything that survives is what was already durable in SQLite.
3. **Timed recovery.** The daemon is restarted on the **same data dir**. The clock
   runs until `/healthz` is up *and* `GET /v1/runs/{id}` shows the run reloaded and
   still `running` with its prior spend — that interval is the **recovery time**. The
   `resumed runs from store` startup line is the daemon's own confirmation it
   restored runs from the store.
4. **Continue to the budget.** The run keeps calling on the same run-id until it
   halts. The proof is three-way: the *after-restart* spend equals the *before-crash*
   spend (no reset, no double-count), and the *final* spend lands on exactly the
   budget with `dollar_budget_exceeded` — i.e. each call is charged **exactly once**
   across the crash. If the meter had reset to `$0`, the run would have spent the
   budget *plus* the pre-crash calls re-paid (the counterfactual the harness prints).

Recovery is sub-100 ms on a laptop here because the work is reloading rows from a
local SQLite file, not replaying a log. The number scales with how many runs and how
much ledger history the store holds, not with how long the killed run had been
spending.

## Scope & honesty

- Two dimensions are measured here: the **cost ceiling** ([`benchmark.py`](benchmark.py))
  and **crash recovery** ([`recovery.py`](recovery.py), above). The crash-resume
  mechanism is also demonstrated interactively in
  [`examples/kill-9-resume`](../examples/kill-9-resume).
- It deliberately removes provider latency/variance to isolate the governance
  effect. The enforcement overhead RiskKernel itself adds is small and measured
  separately; this harness is about dollars, not milliseconds.
- The mock and pricing are in this directory — inspect and change them. Nothing is
  hidden behind a wrapper.
