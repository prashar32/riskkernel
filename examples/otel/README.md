# Observability — OpenTelemetry GenAI export (Surface 3)

RiskKernel emits one OpenTelemetry span per governed model call — and one per
governed MCP **tool call** — carrying the attribute set pinned in
[`api/v1/otel-genai.md`](../../api/v1/otel-genai.md): standard `gen_ai.*` (system,
model, token usage, finish reason) plus the `riskkernel.*` governance extension
(run id, step, **cost in USD**, **budget remaining**, **halt reason**, **tool
status**). Point it at any OTLP backend you already run.

> **No telemetry by default.** RiskKernel exports *nothing* unless you set
> `OTEL_EXPORTER_OTLP_ENDPOINT`. Spans go only to the endpoint you choose. See
> [`SECURITY.md`](../../SECURITY.md).

## 60-second local view (Jaeger)

```bash
# 1. Start a local OTLP backend with a trace UI
docker compose -f examples/otel/docker-compose.yaml up -d

# 2. Run RiskKernel pointed at it
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export ANTHROPIC_API_KEY=sk-ant-...
riskkernel serve

# 3. Make a governed call through the proxy (one env var for your app)
export OPENAI_BASE_URL=http://localhost:7070/v1
#   ... or curl it directly:
curl http://localhost:7070/v1/chat/completions \
  -H 'content-type: application/json' \
  -H 'X-RiskKernel-Run-Id: demo-run' \
  -d '{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}'
```

Open **http://localhost:16686**, pick service **`riskkernel`**, and you'll see a
span named `chat claude-sonnet-4-5`. Open it to see the attributes — including
`riskkernel.cost.usd`, `riskkernel.budget.tokens.remaining`, and (if the run hit a
limit) `riskkernel.halt.reason`.

Governed MCP tool calls land the same way, as `execute_tool {tool}` spans carrying
`gen_ai.tool.name` and `riskkernel.tool.status` — a blocked or denied call is marked
with an error status, so policy refusals stand out in the trace UI next to your
model calls.

## Other backends

Same spans, just change the endpoint:

| Backend | Endpoint | Protocol |
|---|---|---|
| **SigNoz** | `http://localhost:4317` | `grpc` |
| **Grafana Tempo** | `http://localhost:4317` | `grpc` |
| **Honeycomb** | `https://api.honeycomb.io` | `grpc` (+ `x-honeycomb-team` header via std OTEL env) |
| **Datadog Agent** | `http://localhost:4317` | `grpc` |
| **OTel Collector** | your collector's OTLP receiver | `grpc` or `http` |

Use `OTEL_EXPORTER_OTLP_PROTOCOL=http` (and endpoint `http://host:4318`) if your
backend prefers OTLP/HTTP.

## Building cost/usage dashboards

Because cost and budget live on every span as first-class attributes, you can build
panels directly from spans (e.g. in Grafana over Tempo, or SigNoz):

- **Spend per run** — sum `riskkernel.cost.usd` grouped by `riskkernel.run.id`.
- **Token burn rate** — rate of `gen_ai.usage.output_tokens`.
- **Budget headroom** — min `riskkernel.budget.dollars.remaining` per run.
- **Halts** — count of spans where `riskkernel.halt.reason` is present, grouped by
  reason (`token_budget_exceeded`, `time_budget_exceeded`, …).
- **Latency by model** — span duration grouped by `gen_ai.request.model`.
- **Tool refusals** — count of `execute_tool` spans grouped by
  `riskkernel.tool.status` (`blocked` by the allowlist, `denied` at the approval
  gate, or `timeout` vs `approved`).
