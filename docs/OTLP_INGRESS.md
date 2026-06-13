# OTLP trace ingress

RiskKernel emits OpenTelemetry GenAI spans to your observability backend (the
export side of Surface 3). The other half is **ingress**: RiskKernel can also *be*
an OTLP endpoint that consumes GenAI spans from apps already instrumented with
OpenLLMetry, the OpenAI Agents SDK, or the Vercel AI SDK — so it can make spend
visible for agents it never directly proxied.

Point an existing app's OTLP exporter at RiskKernel and the model calls it already
traces show up against governed runs, with tokens and cost metered into the same
ledger the proxy uses.

## Enabling it

The receiver is **off by default** — no listener is mounted unless you turn it on:

```bash
RISKKERNEL_OTEL_INGRESS_ENABLED=true riskkernel serve
```

On the app side, point any OpenTelemetry exporter at the daemon. The endpoint is
the standard OTLP/HTTP traces path, so this is the usual one env var:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:7070
# when RISKKERNEL_API_TOKEN is set, the exporter must carry it:
export OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer $RISKKERNEL_API_TOKEN
```

The exporter appends `/v1/traces` itself. Both OTLP encodings work: protobuf
(`application/x-protobuf`, the SDK default) and JSON (`application/json`).

## Correlating spans to a run

Set `riskkernel.run.id` on your spans (or once on the OTel resource) so consumed
calls are attributed to a governed run:

```python
span.set_attribute("riskkernel.run.id", run_id)
```

The run is created lazily under the default budget if it doesn't exist yet, exactly
like the proxy's run-id header. A GenAI usage span with **no** run id is observed
and reported back as a rejected span in the OTLP partial-success response, but is
not metered.

## What gets metered

For each span carrying token usage, RiskKernel reads the pinned GenAI attributes
(see [`api/v1/otel-genai.md`](../api/v1/otel-genai.md)):

| Attribute | Used for |
|---|---|
| `gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens` | Token counts and cost (priced via the same table the proxy uses). |
| `gen_ai.response.model` (falls back to `gen_ai.request.model`) | The model, for pricing and attribution. |
| `gen_ai.system` | The provider, recorded on the ledger entry. |
| `riskkernel.run.id` | The run to attribute the call to. |

Token counts emitted as a double or numeric string (some instrumenters do this)
are accepted rather than dropped to zero. Spans without usage — tool calls,
retrieval, framework spans — are ignored.

## Scope: observe + meter

Ingested calls already happened in the other app, so the budget can't block them
before the fact. RiskKernel **observes and meters** them: the call is recorded
against the run's ledger, and if the recorded usage crosses the run's budget the
run is marked halted (visible in `riskkernel runs list`, the audit export, and
`GET /v1/runs/{id}`). Actively governing consumed spans — gating or refusing the
*next* call on a run that an external app is driving — is a separate, future step.

For deterministic, before-the-fact enforcement, route calls through the proxy
(Surface 1) or the SDK (Surface 2) instead.
