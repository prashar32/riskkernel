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
| `riskkernel.step.index` | int | `3` | Loop iteration. |
| `riskkernel.cost.usd` | double | `0.0042` | Cost charged to the ledger for this call. |
| `riskkernel.budget.tokens.limit` | int | `200000` | The run's token budget, if set. |
| `riskkernel.budget.tokens.remaining` | int | `184221` | Remaining at the time of the span. |
| `riskkernel.budget.dollars.limit` | double | `5.00` | |
| `riskkernel.budget.dollars.remaining` | double | `4.81` | |
| `riskkernel.halt.reason` | string | `token_budget_exceeded` | Set on the span where the governor halted the run (see HaltReason in openapi.yaml). |
| `riskkernel.approval.required` | bool | `true` | Side-effecting call gated for HITL. |
| `riskkernel.approval.decision` | string | `approve` | Once resolved. |
| `riskkernel.tool.name` | string | `mcp://shell` | For tool-call spans. |
| `riskkernel.tool.side_effect` | string | `write` | Classified side effect. |

## Consumption (ingress)

When acting as an OTLP endpoint, RiskKernel reads incoming `gen_ai.usage.*` to feed
the cost ledger and `gen_ai.request.model` / `gen_ai.system` to attribute spend,
correlating by `riskkernel.run.id` when present (or a configured trace→run mapping
otherwise). This lets RiskKernel govern apps it did not directly instrument.
