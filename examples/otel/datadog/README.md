# Cost & governance dashboard (Datadog)

An importable [Datadog](https://www.datadoghq.com) dashboard for your governed runs
— **spend, token burn, budget halts, tool-call outcomes, and latency by model** —
built entirely from the OpenTelemetry spans RiskKernel already emits. No extra
instrumentation: Datadog ingests the spans over OTLP, aggregates them, and you
import one JSON file.

> Already on Grafana + Tempo? [`../grafana/`](../grafana/) ships the same panels as
> a provisioned stack. On [SigNoz](https://signoz.io)? [`../signoz/`](../signoz/) is
> the SigNoz import. This is the Datadog equivalent.

## Point RiskKernel at Datadog

RiskKernel exports OTLP traces; the **Datadog Agent** ingests OTLP. RiskKernel just
emits OTLP — **the Datadog API key lives on the Agent, not on RiskKernel** (the
runtime never phones home; it only sends spans to the endpoint you point it at).

First, enable the Agent's OTLP receiver (gRPC `4317` / HTTP `4318`) and give the
Agent your `DD_API_KEY` — this is standard Datadog Agent config, the same key the
Agent already uses to forward everything else:

```bash
# On the Datadog Agent (Docker example):
docker run -d --name dd-agent \
  -e DD_API_KEY=$DD_API_KEY \
  -e DD_SITE=datadoghq.com \
  -e DD_APM_ENABLED=true \
  -e DD_OTLP_CONFIG_RECEIVER_PROTOCOLS_GRPC_ENDPOINT=0.0.0.0:4317 \
  -e DD_OTLP_CONFIG_RECEIVER_PROTOCOLS_HTTP_ENDPOINT=0.0.0.0:4318 \
  -p 4317:4317 -p 4318:4318 \
  gcr.io/datadoghq/agent:latest
```

Then point RiskKernel's exporter at the Agent and start the daemon (your keys, as
always) — the usual one env var:

```bash
# RiskKernel -> Datadog Agent OTLP (no Datadog key here; it's on the Agent):
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export ANTHROPIC_API_KEY=sk-ant-...
riskkernel serve

# Then drive some governed traffic — point your app's base URL at the proxy:
export OPENAI_BASE_URL=http://localhost:7070/v1
#   (or run any example under ../../ — the loop-killer, the MCP demo, etc.)
```

Use `OTEL_EXPORTER_OTLP_PROTOCOL=http` with endpoint `http://localhost:4318` if you
prefer OTLP/HTTP. See the OTLP export notes in [`../README.md`](../README.md) for
HTTP-vs-gRPC and other backends.

> **No telemetry by default.** RiskKernel exports *nothing* unless
> `OTEL_EXPORTER_OTLP_ENDPOINT` is set; spans go only to the Datadog Agent you point
> it at. See [`SECURITY.md`](../../../SECURITY.md).

## Import the dashboard

In the Datadog UI: **Dashboards → New Dashboard → Import dashboard JSON** (or the
gear menu → **Import dashboard JSON** on an existing one), and upload
[`dashboards/riskkernel-agent-runs.json`](dashboards/riskkernel-agent-runs.json).
Spend, halts, and tool outcomes fill in as runs execute (default range: last 1h —
adjust with the time picker).

## What each panel is

Datadog references **span attributes with an `@` prefix** (tags have none), so the
queries read `@riskkernel.cost.usd`, `@gen_ai.request.model`, and friends — exactly
the names RiskKernel emits.

| Panel | Span query (`data_source: spans`) |
|---|---|
| Total spend / Output tokens / Model calls | `sum(@riskkernel.cost.usd)`, `sum(@gen_ai.usage.output_tokens)`, `count` of `@gen_ai.operation.name:chat` |
| **Tool calls refused** | `count` over `@gen_ai.operation.name:execute_tool -@riskkernel.tool.status:approved` |
| Spend over time | `sum(@riskkernel.cost.usd)` over time |
| Spend by run | `sum(@riskkernel.cost.usd)` group by `@riskkernel.run.id` (toplist) |
| p95 latency by model | `pc95(@duration)` group by `@gen_ai.request.model` |
| Model-call rate by model | `count` of `chat` spans group by `@gen_ai.request.model` |
| Output tokens by model | `sum(@gen_ai.usage.output_tokens)` group by `@gen_ai.request.model` |
| Tool calls by outcome | `count` of `execute_tool` spans group by `@riskkernel.tool.status` (approved / blocked / denied / timeout) |
| Budget halts by reason | `count` over `@riskkernel.halt.reason:*` group by `@riskkernel.halt.reason` |

Every attribute these queries touch is pinned in
[`api/v1/otel-genai.md`](../../../api/v1/otel-genai.md) — the names are a stable
public contract, so the panels keep working across RiskKernel upgrades.

## Notes & assumptions

A few Datadog-specific things to know:

- **Service name → the `service` tag.** OTLP's `service.name` resource attribute
  becomes Datadog's `service`. RiskKernel sets it to `riskkernel`, so the dashboard
  ships a `$service` template variable defaulting to `riskkernel`. If you've
  overridden the service name, pick yours from the variable (or edit its default);
  if you run a single Datadog org for RiskKernel only, you can clear the filter.
- **The `@` prefix.** This is the one thing people trip on: span *attributes* need
  the leading `@` (`@riskkernel.cost.usd`); span *tags* (host/env/service) do not.
  The dotted attribute names are preserved by Datadog's OTLP intake — they are not
  flattened to underscores — so they read identically to the pinned set.
- **`@duration`.** The latency panel uses Datadog's intrinsic span-duration measure
  (`@duration`, **nanoseconds**) — span duration is intrinsic, not a RiskKernel
  attribute.
- **Indexed spans + retention.** Dashboard widgets with `data_source: spans` query
  the spans Datadog **retains** (via your retention filters). Ad-hoc span analytics
  has a limited retention window, so make sure a retention filter keeps RiskKernel's
  spans (e.g. `service:riskkernel`) if you want history beyond it.
- **Long-term cost aggregation → a span-based metric (optional).** For durable,
  long-window spend rollups (and faster dashboards), generate a **span-based custom
  metric** from `@riskkernel.cost.usd` once, then point the spend panels at that
  metric instead of the raw spans. In **APM → Generate Metrics → New Metric**, set
  the query to `service:riskkernel @riskkernel.cost.usd:*`, aggregate the
  `@riskkernel.cost.usd` value as `sum`, and add `@riskkernel.run.id` (and any
  `@riskkernel.run.meta.*` tag) as a group-by — then the "Spend over time" / "Spend
  by run" panels can switch their `data_source` from `spans` to `metrics`. The
  shipped JSON uses raw spans so it works on import with no setup; the metric is the
  upgrade path for retention and scale. Note that custom metrics are billable.
- **Empty panels?** That almost always means no spans match the filter yet — drive
  some governed traffic, widen the time range, confirm the `$service` variable
  matches your deployment, and confirm a retention filter is keeping the spans. The
  tool/halt panels stay empty until a tool call or a budget halt actually happens.
