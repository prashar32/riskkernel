# Metrics

RiskKernel exposes a Prometheus `/metrics` endpoint describing the daemon's
governed-run state — runs by status, halt reasons, total spend and tokens, and the
human-in-the-loop approval-queue depth. Platform teams scrape it to watch a
reliability tool's own health alongside everything else they run.

It honors the no-telemetry posture: this is **local** metrics the user scrapes.
Nothing is emitted anywhere; the numbers are derived on the fly from the SQLite
state the user already owns, and no prompt content or PII is exposed.

## The endpoint

`GET /metrics` returns the Prometheus text exposition format (version 0.0.4). It's
authenticated like the rest of the API — when `RISKKERNEL_API_TOKEN` is set, send
the bearer token. It's served only when a durable store is configured (the default
SQLite state).

```bash
curl -s -H "Authorization: Bearer $RISKKERNEL_API_TOKEN" http://localhost:7070/metrics
```

## What's exposed

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `riskkernel_runs_total` | gauge | `status` | Governed runs by lifecycle status (`running` / `halted` / `cancelled`). |
| `riskkernel_runs_halted_total` | gauge | `reason` | Halted runs by halt reason (`token_budget_exceeded`, `dollar_budget_exceeded`, `loop_budget_exceeded`, `time_budget_exceeded`, `cancelled`). |
| `riskkernel_spend_dollars_total` | counter | — | Total spend in dollars across all runs, summed from the cost ledger. |
| `riskkernel_tokens_total` | counter | — | Total tokens (prompt + completion) across all runs. |
| `riskkernel_model_calls_total` | counter | — | Priced model calls recorded in the cost ledger. |
| `riskkernel_approvals_pending` | gauge | — | Pending human-in-the-loop approvals (the queue depth). |

Example scrape:

```
# HELP riskkernel_runs_total Number of governed runs by lifecycle status.
# TYPE riskkernel_runs_total gauge
riskkernel_runs_total{status="cancelled"} 1
riskkernel_runs_total{status="halted"} 3
riskkernel_runs_total{status="running"} 5
# HELP riskkernel_runs_halted_total Number of halted runs by halt reason.
# TYPE riskkernel_runs_halted_total gauge
riskkernel_runs_halted_total{reason="dollar_budget_exceeded"} 2
riskkernel_runs_halted_total{reason="loop_budget_exceeded"} 1
# HELP riskkernel_spend_dollars_total Total spend in dollars across all runs, summed from the cost ledger.
# TYPE riskkernel_spend_dollars_total counter
riskkernel_spend_dollars_total 4.21
# HELP riskkernel_tokens_total Total tokens (prompt + completion) across all runs.
# TYPE riskkernel_tokens_total counter
riskkernel_tokens_total 182340
# HELP riskkernel_model_calls_total Total priced model calls recorded in the cost ledger.
# TYPE riskkernel_model_calls_total counter
riskkernel_model_calls_total 96
# HELP riskkernel_approvals_pending Number of pending human-in-the-loop approvals.
# TYPE riskkernel_approvals_pending gauge
riskkernel_approvals_pending 0
```

## Example scrape config

Add a job to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: riskkernel
    scrape_interval: 30s
    static_configs:
      - targets: ["localhost:7070"]
    # Only needed when RISKKERNEL_API_TOKEN is set.
    authorization:
      type: Bearer
      credentials: "<your RISKKERNEL_API_TOKEN>"
```

For the enforcement overhead a platform team cares about most — the latency
RiskKernel adds in front of each call — see [`PERFORMANCE.md`](PERFORMANCE.md):
the deterministic decision is ~150 ns with zero allocations, so it never shows up
as a meaningful contributor next to the model call itself.
