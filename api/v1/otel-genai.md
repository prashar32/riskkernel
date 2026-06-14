# OpenTelemetry GenAI Attribute Set (pinned)

This file pins the OpenTelemetry semantic-convention attribute set that RiskKernel
both **emits** (Surface 3 egress, to your observability backend) and **consumes**
(Surface 3 ingress, governing apps already instrumented by OpenLLMetry / the
OpenAI Agents SDK / the Vercel AI SDK). It is part of the public contract
(COMPATIBILITY.md): these attribute names are stable across minor versions.

## Pinned spec version

- **OpenTelemetry Semantic Conventions for Generative AI**: `v1.27.0`
  (the `gen_ai.*` namespace).
- Rationale: pinning a specific version means a user's dashboards and alerts keep
  working across RiskKernel upgrades. When we move to a newer convention version,
  it is a documented change in `CHANGELOG.md` with a deprecation window per
  COMPATIBILITY.md, and we emit both old and new names during the overlap where
  feasible.

## Span shape

One span per **model call** (`gen_ai.client` kind), nested under one span per
**step**, nested under one span per **run**. Span names follow the convention
`{gen_ai.operation.name} {gen_ai.request.model}`, e.g. `chat claude-sonnet-4-5`.

One span per **governed MCP tool call** (`gen_ai.operation.name` = `execute_tool`),
named `execute_tool {tool}`, e.g. `execute_tool write_file`. It carries the
governance outcome in `riskkernel.tool.status`, so allowlist blocks and approval
denials are visible alongside model calls — a refused call is marked with an error
span status.

## Emitted attributes (`gen_ai.*` — standard)

| Attribute | Type | Example | Notes |
|---|---|---|---|
| `gen_ai.system` | string | `anthropic` | Provider. |
| `gen_ai.operation.name` | string | `chat` | `chat`, `text_completion`, `embeddings`. |
| `gen_ai.request.model` | string | `claude-sonnet-4-5` | Requested model. |
| `gen_ai.response.model` | string | `claude-sonnet-4-5-20250...` | Model that actually served. |
| `gen_ai.request.max_tokens` | int | `1024` | If set by the caller. |
| `gen_ai.request.temperature` | double | `0.7` | If set. |
| `gen_ai.usage.input_tokens` | int | `812` | Prompt tokens. |
| `gen_ai.usage.output_tokens` | int | `134` | Completion tokens. |
| `gen_ai.response.finish_reasons` | string[] | `["stop"]` | |
| `gen_ai.response.id` | string | `msg_01...` | Provider response id. |
| `gen_ai.tool.name` | string | `write_file` | On tool-call spans: the tool invoked. |
| `error.type` | string | `provider_error` | On failure (standard OTel). |

> Prompt/response **content** is NOT emitted by default (privacy + no telemetry
> posture). Capturing content as span events is opt-in via config and never leaves
> the user's configured OTLP endpoint.

## RiskKernel attributes (`riskkernel.*` — our governance extension)

These carry the deterministic-governance dimensions that the standard GenAI
conventions don't model. Names are stable per COMPATIBILITY.md.

| Attribute | Type | Example | Notes |
|---|---|---|---|
| `riskkernel.run.id` | string | `9f1c…` | Correlates every span to a governed run. |
| `riskkernel.run.name` | string | `nightly-report` | The run's name, for grouping spend by run (set when non-empty). |
| `riskkernel.run.meta.<key>` | string | `riskkernel.run.meta.team` = `payments` | One attribute per user-supplied run metadata tag, so spend can be grouped by team/user/feature in the backend without a separate run→tag map. Cardinality is the user's own. |
| `riskkernel.step.index` | int | `3` | Loop iteration. |
| `riskkernel.cost.usd` | double | `0.0042` | Cost charged to the ledger for this call. |
| `riskkernel.budget.tokens.limit` | int | `200000` | The run's token budget, if set. |
| `riskkernel.budget.tokens.remaining` | int | `184221` | Remaining at the time of the span. |
| `riskkernel.budget.dollars.limit` | double | `5.00` | |
| `riskkernel.budget.dollars.remaining` | double | `4.81` | |
| `riskkernel.halt.reason` | string | `token_budget_exceeded` | Set on the span where the governor halted the run (see HaltReason in openapi.yaml). |
| `riskkernel.tool.side_effect` | string | `write` | On tool-call spans: the classified side effect (empty = read-only). |
| `riskkernel.tool.status` | string | `blocked` | On tool-call spans: `approved`, `blocked` (allowlist), `denied` (approval), or `timeout`. |

## Consumption (ingress)

RiskKernel can act as an OTLP/HTTP trace endpoint at **`POST /v1/traces`** — the
standard OTLP path, so any exporter targets it with one env var
(`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:7070`). It accepts both protobuf
(`application/x-protobuf`, the OTLP default) and JSON (`application/json`) and
replies with an OTLP `ExportTraceServiceResponse` in the same encoding.

For each span carrying token usage (`gen_ai.usage.input_tokens` /
`gen_ai.usage.output_tokens`), it reads `gen_ai.response.model` (falling back to
`gen_ai.request.model`) and `gen_ai.system` and meters the call's tokens and cost
into the ledger, correlating to a governed run by `riskkernel.run.id` (taken from
the span, falling back to the resource). A span without a run id is observed and
reported as a rejected span in the OTLP partial-success response, but not metered.
Spans without usage (tool calls, retrieval, framework spans) are ignored.

This lets RiskKernel make spend visible for apps it did not directly proxy. Scope
is **observe + meter**: a consumed span records against the run's ledger (and marks
the run halted if its budget is crossed) but does not retroactively block a call
that already happened — governing consumed spans is a separate, future step.

The receiver is **off by default**; no listener is mounted unless
`RISKKERNEL_OTEL_INGRESS_ENABLED` is set. It is authenticated like the rest of the
API, so an exporter must carry the bearer token via
`OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer <token>` when a token is set.
