# Cost & governance dashboard (SigNoz)

An importable [SigNoz](https://signoz.io) dashboard for your governed runs —
**spend, token burn, budget halts, tool-call outcomes, and latency by model** —
built entirely from the OpenTelemetry spans RiskKernel already emits. No extra
instrumentation, no metrics pipeline: SigNoz aggregates the spans, and you import
one JSON file.

> Already on Grafana + Tempo? [`../grafana/`](../grafana/) ships the same panels as
> a provisioned stack. On [Datadog](https://www.datadoghq.com)?
> [`../datadog/`](../datadog/) is the Datadog import. This is the SigNoz equivalent
> for teams who already run it.

## Point RiskKernel at SigNoz

RiskKernel exports OTLP traces; SigNoz ingests OTLP. So this is the usual one env
var — point the exporter at your SigNoz OTLP endpoint and start the daemon (your
keys, as always):

```bash
# Self-hosted SigNoz (OTLP collector listens on 4317 gRPC / 4318 HTTP):
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export ANTHROPIC_API_KEY=sk-ant-...
riskkernel serve

# Then drive some governed traffic — point your app's base URL at the proxy:
export OPENAI_BASE_URL=http://localhost:7070/v1
#   (or run any example under ../../ — the loop-killer, the MCP demo, etc.)
```

**SigNoz Cloud** (or any endpoint that needs a key) uses the standard OTLP env
vars — set the ingestion key as a header:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.<region>.signoz.cloud:443
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_HEADERS="signoz-ingestion-key=$SIGNOZ_INGESTION_KEY"
```

Header values carry secrets and are never logged. See the OTLP export notes in
[`../README.md`](../README.md) for HTTP-vs-gRPC and other backends.

> **No telemetry by default.** RiskKernel exports *nothing* unless
> `OTEL_EXPORTER_OTLP_ENDPOINT` is set; spans go only to the SigNoz you point it
> at. See [`SECURITY.md`](../../../SECURITY.md).

## Import the dashboard

In the SigNoz UI: **Dashboards → New dashboard → Import JSON**, and upload
[`dashboards/riskkernel-agent-runs.json`](dashboards/riskkernel-agent-runs.json).
Spend, halts, and tool outcomes fill in as runs execute (default range: last 1h,
adjust with the time picker).

## What each panel is

| Panel | Trace query (Query Builder) |
|---|---|
| Total spend / Output tokens / Model calls | `sum(riskkernel.cost.usd)`, `sum(gen_ai.usage.output_tokens)`, `count()` |
| **Tool calls refused** | `count()` over `execute_tool` spans where `riskkernel.tool.status != 'approved'` |
| Spend over time / Spend by run | `sum(riskkernel.cost.usd)`, grouped by `riskkernel.run.id` |
| p95 latency by model | `p95(durationNano)` group by `gen_ai.request.model` |
| Model-call rate by model | `rate()` group by `gen_ai.request.model` |
| Output tokens by model | `sum(gen_ai.usage.output_tokens)` group by `gen_ai.request.model` |
| Tool calls by outcome | `count()` group by `riskkernel.tool.status` (approved / blocked / denied / timeout) |
| Budget halts by reason | `count()` over spans where `riskkernel.halt.reason EXISTS`, group by reason |

Every attribute these queries touch is pinned in
[`api/v1/otel-genai.md`](../../../api/v1/otel-genai.md) — the names are a stable
public contract, so the panels keep working across RiskKernel upgrades.

## Notes & assumptions

This dashboard targets **SigNoz Query Builder v5** (the dot-preserving attribute
schema), which references OpenTelemetry attributes by their original dotted names —
e.g. `riskkernel.cost.usd`, `gen_ai.request.model` — exactly as RiskKernel emits
them. A few things to know:

- **Service name.** The summary panels filter on
  `resource.service.name = 'riskkernel'`, the service name RiskKernel sets on the
  spans it exports. If you run multiple services through one SigNoz, this scopes the
  totals to RiskKernel; if you've overridden the service name, adjust the filter (or
  drop it) in those panels.
- **`durationNano`.** Latency panels use SigNoz's built-in span-duration column
  (`durationNano`, nanoseconds) — span duration is intrinsic, not a RiskKernel
  attribute.
- **Older SigNoz (pre-v5).** If your SigNoz predates the v5 query builder /
  dot-preserving schema, attribute keys may have been normalized to underscores
  (e.g. `gen_ai_request_model`). Upgrade SigNoz, or edit the attribute keys in the
  query builder after import. The pinned RiskKernel attribute names don't change;
  only SigNoz's storage convention does.
- **Empty panels?** That almost always means no spans match the filter yet — drive
  some governed traffic, widen the time range, or confirm the service-name filter
  matches your deployment. The tool/halt panels stay empty until a tool call or a
  budget halt actually happens.
