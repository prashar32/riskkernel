# Enforcement overhead

RiskKernel's value is a *deterministic* check in front of every model and tool call.
The fair question a platform team asks is: **how much does that check cost?**

**The deterministic enforcement decision adds ~150 nanoseconds and zero heap
allocations per governed call.** It's a mutex, a handful of integer/float
comparisons, and a counter update — no I/O, no network, and (by design) no LLM in
the decision path. It is not a meaningful contributor to an agent's latency, which
is dominated by the model call itself (tens to thousands of *milliseconds*).

## Measured

`go test -bench`, Apple M5 Max (arm64), Go's default settings:

| Guard | What it does | Time | Allocations |
|---|---|---|---|
| `PreStep()` | register a loop iteration; enforce loop + time budget | ~48 ns/op | 0 |
| `CanProceed()` | enforce the token/dollar/time ceiling before a call | ~48 ns/op | 0 |
| `RecordUsage()` | meter a completed call; re-evaluate budgets | ~48 ns/op | 0 |
| **one governed step** | all three around a single model call | **~144 ns/op** | **0** |

Zero allocations means no GC pressure from the hot path regardless of throughput.

## Reproduce

```bash
go test -run '^$' -bench . -benchmem ./internal/governor/
```

The benchmarks live in [`internal/governor/bench_test.go`](../internal/governor/bench_test.go).
They run each guard on its **allow path** with all four budgets active (set near
their type maxima so the comparisons all execute without halting) — the steady state
that fronts a real call. Absolute nanoseconds vary by CPU; the shape (sub-microsecond,
zero-allocation) does not.

## Why it's this cheap — and why that matters

The enforcement decision is pure in-memory arithmetic under a single mutex. There is
no database round-trip on the hot path (persistence is best-effort write-through on a
background context, off the decision), no allocation, and **no model call** — the
governor never asks an LLM whether something is allowed. That last point is the
determinism property the whole product rests on: the same state always yields the
same allow/deny, fast enough to be free.

## Scope of this number

This measures the **deterministic enforcement decision** — the work RiskKernel
uniquely adds. It deliberately does not fold in:

- **The proxy/network hop** (Surface 1) — a local HTTP round-trip, dominated by your
  network to the provider, not by RiskKernel.
- **Provider latency** — the model call itself, orders of magnitude larger.
- **Best-effort persistence** — the SQLite write-through that makes a run auditable
  and crash-resumable runs on a background context, off the enforcement path.

The honest claim is narrow and strong: the *decision* RiskKernel inserts in front of
every call is sub-microsecond and allocation-free.
