# Budgets — the public contract

This document defines RiskKernel's budget surface: the four budget dimensions,
everywhere a budget can be set, the precedence between them, what happens when a
limit trips, and what we promise to keep stable. If behavior and this document
ever disagree, that's a bug — [open an issue](https://github.com/prashar32/riskkernel/issues).

Budgets are the headline feature: **deterministic, run-level hard limits**
enforced in Go before every model call and loop iteration. The same state always
produces the same allow/halt decision — no LLM is ever consulted.

## The four dimensions

Every budget is per-run and has the same shape on every surface:

| Dimension | Field / env suffix | Type | Meaning |
|---|---|---|---|
| Tokens | `tokens` / `_TOKENS` | int64 | Max total tokens (prompt + completion) across the run |
| Dollars | `dollars` / `_DOLLARS` | float | Max cost in USD across the run, from the cost ledger |
| Loops | `loops` / `_LOOPS` | int32 | Max agent loop iterations (steps) |
| Seconds | `seconds` / `_SECONDS` | int32 | Max wall-clock duration of the run |

Each limit is enforced independently; **the first to trip halts the run.** A
value of `0` (or an omitted field) means *unlimited* for that dimension —
subject to the safe defaults below.

## Safe defaults (out-of-the-box behavior)

A reliability runtime must be safe before it is configured. When the daemon
starts with **no** `RISKKERNEL_DEFAULT_*` variable set, runs created without an
explicit budget get the **safe default budget**:

| | Safe default |
|---|---|
| Dollars | **$5.00** per run |
| Loops | **100** per run |
| Seconds | **3600** (1 hour) per run |
| Tokens | unlimited (dollars caps spend) |

The daemon logs this prominently at startup. Setting **any**
`RISKKERNEL_DEFAULT_*` variable — even to `0` — is explicit control and
disables the safe defaults entirely: each set value is used, and unset values
mean unlimited. `RISKKERNEL_DEFAULT_DOLLARS=0` therefore restores fully
unlimited runs, deliberately.

## Where a budget can be set (precedence, highest first)

1. **Per-run, inline** — the `budget` object on `POST /v1/runs` (or
   `@governed_run(budget=...)` in the SDK). Overrides everything below,
   field-by-field.
2. **Policy bundle** — a named bundle registered via `POST /v1/policies` and
   referenced by `policyRef` at run creation. An inline `budget` overrides the
   bundle's default budget field-by-field.
3. **Daemon default** — `RISKKERNEL_DEFAULT_TOKENS` / `_DOLLARS` / `_LOOPS` /
   `_SECONDS`. Applied to runs created with neither an inline budget nor a
   `policyRef` — e.g. proxy calls that supply only a run-id.
4. **Safe defaults** — only when the daemon default is entirely unconfigured
   (see above).

## Surface 1 — Proxy (zero code)

The proxy enforces the budget on every intercepted call. Group calls into a run
with one request header; read enforcement state from the response headers:

```
request:   X-RiskKernel-Run-Id: <your-run-id>     # lazily creates the run under the default budget
response:  X-RiskKernel-Cost-Usd, X-RiskKernel-Tokens, X-RiskKernel-Step, X-RiskKernel-Halt-Reason
```

Calls without a run-id header get a fresh ephemeral run (named `proxy`) under
the default budget. When any limit trips, the call returns **HTTP 402** with the
halt reason; no model call escapes the cap, and the run's state is persisted.

The proxy alone cannot count your agent's *outer* loop — it sees model calls,
not loop iterations. Loop budgets through the proxy cap model calls per run;
for true loop-iteration enforcement use the SDK.

## Surface 2 — Python SDK (deep control)

```python
from riskkernel import Budget, governed_run

@governed_run(budget=Budget(dollars=2.50, loops=20, seconds=600))
def my_agent():
    run = current_run()
    while True:
        run.step()          # registers a loop iteration; raises BudgetExceeded when spent
        ...                 # your model/tool calls, via the proxy or directly
```

`run.step()` calls `POST /v1/runs/{id}/steps`, which increments the loop counter
and enforces the loop + time budgets in the Go core — the SDK carries no
enforcement logic. `BudgetExceeded` is raised on 402; catch it for graceful
shutdown, or let it terminate the run.

## Surface 3 — REST API

```
POST /v1/runs                  {"budget": {"dollars": 2.5, "loops": 20}, ...}   # or "policyRef"
POST /v1/runs/{id}/steps       402 + HaltReason when loop/time budget is spent
POST /v1/runs/{id}/cancel      manual kill switch
GET  /v1/runs/{id}             status, usage (tokens/dollars/loops/elapsedSeconds), haltReason
```

## Halt semantics

When a limit trips, deterministically and atomically:

1. The triggering call is refused — **HTTP 402** with a machine-readable reason.
2. The run's status becomes `halted`, with `haltReason` one of
   `token_budget_exceeded` | `dollar_budget_exceeded` | `loop_budget_exceeded` |
   `time_budget_exceeded`.
3. The halt and final usage are **persisted** — `riskkernel runs list`, the API,
   and the audit export all show the halted state and the spend at halt.
4. A halted run never silently continues. Crash-resume
   (`riskkernel runs resume <id>`) is for *interrupted* runs; a budget-halted
   run stays halted — start a new run with a bigger budget if that's what you
   decide.

Enforcement is checked **before every call and loop iteration** against the
cumulative ledger: once the ceiling is reached, no further call begins. Cost
per call is priced from provider-reported usage via the pricing table
(config-updatable — provider pricing drifts), so a single in-flight call can
land at, but never be issued past, the ceiling.

## Stability promise

The following are stable per [`COMPATIBILITY.md`](../COMPATIBILITY.md) across
all v0.x minor versions:

- The `Budget` schema field names and semantics (`tokens`, `dollars`, `loops`,
  `seconds`; zero/omitted = unlimited).
- The `RISKKERNEL_DEFAULT_*` environment variables and their explicit-control
  semantics.
- The `X-RiskKernel-*` header names.
- HTTP 402 as the budget-halt status and the `HaltReason` enum values above
  (values may be *added*, never renamed or removed within a major version).
- SDK: `Budget`, `governed_run(budget=...)`, `run.step()`, `BudgetExceeded`.

The *safe default values* ($5 / 100 loops / 1h) are sane-default policy, not
contract: they may be tuned in a minor release (with a CHANGELOG entry), since
anyone depending on exact limits should set them explicitly.
