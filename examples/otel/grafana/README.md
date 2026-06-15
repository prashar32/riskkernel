# Cost & governance dashboard (Grafana + Tempo)

A ready-made Grafana dashboard for your governed runs — **spend, token burn,
budget halts, tool-call outcomes, and latency by model** — built entirely from the
OpenTelemetry spans RiskKernel already emits. No extra instrumentation, no metrics
pipeline: [Grafana Tempo](https://grafana.com/oss/tempo/) aggregates the spans with
TraceQL metrics, and the dashboard is provisioned so it shows up on first load.

> The [Jaeger quick-look](../docker-compose.yaml) one directory up is for reading
> individual traces. This is the aggregate view — the panels a platform team watches.
> On [SigNoz](https://signoz.io) instead? [`../signoz/`](../signoz/) is the same
> dashboard, importable there; on [Datadog](https://www.datadoghq.com)?
> [`../datadog/`](../datadog/) ships it as a Datadog dashboard JSON.

![panels: total spend, output tokens, model calls, tool calls refused, spend over
time, spend by run, p95 latency by model, model-call rate, tool calls by outcome,
budget halts by reason]

## Run it

```bash
# 1. Bring up Tempo (OTLP in) + Grafana (dashboard provisioned)
docker compose -f examples/otel/grafana/docker-compose.yaml up -d

# 2. Point RiskKernel at Tempo and start it (your keys, as always)
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export ANTHROPIC_API_KEY=sk-ant-...
riskkernel serve

# 3. Drive some governed traffic — point your app's base URL at the proxy:
export OPENAI_BASE_URL=http://localhost:7070/v1
#   (or run any example under ../../ — the loop-killer, the MCP demo, etc.)
```

Open **http://localhost:3000** → **Dashboards** → **RiskKernel — agent runs**.
Spend, halts, and tool outcomes fill in as runs execute (default range: last 1h).

> **No telemetry by default.** RiskKernel exports nothing unless
> `OTEL_EXPORTER_OTLP_ENDPOINT` is set; spans go only to the Tempo you run here. See
> [`SECURITY.md`](../../../SECURITY.md). The Grafana in this stack is login-free for
> convenience — don't expose it as-is.

## What each panel is

| Panel | TraceQL metric |
|---|---|
| Total spend / Output tokens / Model calls | `sum_over_time(span.riskkernel.cost.usd)`, `sum_over_time(span.gen_ai.usage.output_tokens)`, `count_over_time()` |
| **Tool calls refused** | `count_over_time()` over `execute_tool` spans where `riskkernel.tool.status != "approved"` |
| Spend over time / Spend by run | `sum_over_time(span.riskkernel.cost.usd)`, grouped `by (span.riskkernel.run.id)` |
| p95 latency by model | `quantile_over_time(duration, .95) by (span.gen_ai.request.model)` |
| Model-call rate by model | `rate() by (span.gen_ai.request.model)` |
| Tool calls by outcome | `count_over_time() by (span.riskkernel.tool.status)` (approved / blocked / denied / timeout) |
| Budget halts by reason | `count_over_time() by (span.riskkernel.halt.reason)` |

Every attribute these queries touch is pinned in
[`api/v1/otel-genai.md`](../../../api/v1/otel-genai.md).

## Using it against your own Grafana

The dashboard is a plain Grafana JSON
([`dashboards/riskkernel-agent-runs.json`](dashboards/riskkernel-agent-runs.json)).
To use your existing stack instead of this compose: import the JSON and point its
**Tempo** data source at any Tempo with
[TraceQL metrics enabled](https://grafana.com/docs/tempo/latest/metrics-from-traces/)
(the `local-blocks` processor — see [`tempo.yaml`](tempo.yaml)). Needs Grafana
10.4+ and Tempo 2.7+ (the dashboard uses `sum_over_time`).
