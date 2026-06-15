# Providers — native, and the long tail via LiteLLM

RiskKernel implements the top providers **natively** in Go: Anthropic, OpenAI, and
Ollama (local). For those, point your app at the proxy and you're done — no extra
moving parts.

The other 100+ providers (Google Gemini, Cohere, Mistral, Groq, Together, Azure
OpenAI, AWS Bedrock, OpenRouter, …) are not reimplemented inside RiskKernel — that
isn't the product. Instead you front them with **[LiteLLM](https://github.com/BerriAI/litellm)**,
which already speaks all of them through one OpenAI-compatible endpoint, and put
RiskKernel **in front of LiteLLM**. RiskKernel governs every call; LiteLLM does the
provider routing.

```
your app  →  RiskKernel               →  LiteLLM proxy        →  the real provider
             (budgets, approvals,         (routing to 100+        (Gemini, Cohere,
              audit, checkpoints,           providers, keys)        Mistral, …)
              cost metering, OTel)
```

This is the same shape the [`examples/quickstart-compose`](../examples/quickstart-compose)
demo already proves — it points `RISKKERNEL_OPENAI_BASE_URL` at a one-reply mock
upstream. LiteLLM is just the real-world version of that upstream.

## When to use which

| You want to call… | Do this |
|---|---|
| Anthropic (`claude-*`) | Native. Set `ANTHROPIC_API_KEY` on the daemon; use a `claude-*` model. |
| OpenAI (`gpt-*`, `o1`, `o3`) | Native. Set `OPENAI_API_KEY` on the daemon; use a `gpt-*`/`o1`/`o3` model. |
| A local Ollama model | Native. Set `RISKKERNEL_OLLAMA_BASE_URL`; use a model the routing sends to Ollama. |
| Anything else (Gemini, Cohere, Mistral, Groq, Bedrock, …) | Front it with **LiteLLM** as described below. |

You only need LiteLLM for the long tail. If your stack is purely Anthropic/OpenAI/
Ollama, skip this page.

## How it works

RiskKernel's native **OpenAI provider** has a configurable upstream base URL,
`RISKKERNEL_OPENAI_BASE_URL`. The OpenAI provider POSTs to
`<base>/v1/chat/completions`. LiteLLM's proxy serves exactly that path on
`/v1/chat/completions`, so pointing `RISKKERNEL_OPENAI_BASE_URL` at the LiteLLM
proxy makes RiskKernel forward every "openai" call to LiteLLM, which then routes to
the real provider using the keys **stored in LiteLLM** (never in RiskKernel).

```
RISKKERNEL_OPENAI_BASE_URL=http://litellm:4000
                           └────────────────────┘
   RiskKernel POSTs to  http://litellm:4000/v1/chat/completions
```

> **Routing caveat — read this.** RiskKernel decides which native provider handles a
> request **by model-name prefix**: `claude-*` → Anthropic, `gpt-*`/`o1`/`o3` →
> OpenAI, everything else → the **default provider**. A long-tail LiteLLM model name
> like `gemini-1.5-pro` or `command-r` does **not** match the `gpt-*` prefix, so it
> would fall through to the default provider. To send arbitrary model names to the
> OpenAI provider (and thus to LiteLLM), set **`RISKKERNEL_DEFAULT_PROVIDER=openai`**
> so the fall-through lands on the LiteLLM-backed OpenAI provider. Then any model
> name LiteLLM understands is forwarded as-is. (If you still want native Anthropic
> alongside, `claude-*` names keep routing natively; only the unmatched names go to
> LiteLLM.)

> **The OpenAI provider must be activated.** RiskKernel only registers the OpenAI
> provider when `OPENAI_API_KEY` is set. With LiteLLM upstream you still set it —
> use LiteLLM's **master key** (e.g. `sk-litellm-...`) so RiskKernel authenticates to
> the LiteLLM proxy, not to OpenAI. The real provider keys live in LiteLLM's config,
> not here.

## Configuration

Three knobs on the RiskKernel daemon, one on your app:

| Where | Variable | Value | Why |
|---|---|---|---|
| RiskKernel | `RISKKERNEL_OPENAI_BASE_URL` | `http://litellm:4000` | Forward "openai" calls to the LiteLLM proxy. |
| RiskKernel | `OPENAI_API_KEY` | LiteLLM master key (or any non-empty value if LiteLLM has no auth) | Activates the OpenAI provider; sent to LiteLLM as the bearer token. |
| RiskKernel | `RISKKERNEL_DEFAULT_PROVIDER` | `openai` | Route long-tail model names to the LiteLLM-backed OpenAI provider (see the routing caveat). |
| Your app | `OPENAI_BASE_URL` | `http://localhost:7070/v1` | The usual one env var — your app calls RiskKernel unchanged. |

LiteLLM holds the **real** provider keys (`GEMINI_API_KEY`, `COHERE_API_KEY`,
`MISTRAL_API_KEY`, …) in its own environment / config — RiskKernel never sees them.

### docker-compose example

```yaml
services:
  litellm:
    image: ghcr.io/berriai/litellm:main-latest
    command: ["--config", "/etc/litellm/config.yaml"]
    volumes:
      - ./litellm-config.yaml:/etc/litellm/config.yaml:ro
    environment:
      # The real long-tail provider keys live HERE, not in RiskKernel.
      GEMINI_API_KEY: ${GEMINI_API_KEY}
      COHERE_API_KEY: ${COHERE_API_KEY}
      LITELLM_MASTER_KEY: ${LITELLM_MASTER_KEY}   # e.g. sk-litellm-...

  riskkernel:
    image: ghcr.io/prashar32/riskkernel:latest
    depends_on: [litellm]
    ports:
      - "7070:7070"
    volumes:
      - ./data:/data
      # Optional: prices for long-tail models (see "Cost accuracy" below).
      - ./pricing.json:/etc/riskkernel/pricing.json:ro
    environment:
      RISKKERNEL_OPENAI_BASE_URL: "http://litellm:4000"
      OPENAI_API_KEY: ${LITELLM_MASTER_KEY}        # authenticates to LiteLLM
      RISKKERNEL_DEFAULT_PROVIDER: "openai"
      RISKKERNEL_DEFAULT_DOLLARS: "5"              # a hard $5/run ceiling
      RISKKERNEL_PRICING_FILE: "/etc/riskkernel/pricing.json"
```

### Minimal LiteLLM `config.yaml`

```yaml
model_list:
  - model_name: gemini-1.5-pro          # the name your app sends to RiskKernel
    litellm_params:
      model: gemini/gemini-1.5-pro      # how LiteLLM routes it
      api_key: os.environ/GEMINI_API_KEY
  - model_name: command-r
    litellm_params:
      model: cohere/command-r
      api_key: os.environ/COHERE_API_KEY

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
```

### A governed call

Your app changes nothing but `OPENAI_BASE_URL`; the model name is whatever LiteLLM
exposes:

```bash
export OPENAI_BASE_URL=http://localhost:7070/v1

curl -s -D- http://localhost:7070/v1/chat/completions \
  -H 'content-type: application/json' \
  -H 'X-RiskKernel-Run-Id: gemini-demo' \
  -d '{"model":"gemini-1.5-pro","messages":[{"role":"user","content":"hi"}]}'
# → X-RiskKernel-Cost-Usd, X-RiskKernel-Tokens, X-RiskKernel-Step …
# the run is killed with HTTP 402 the moment it exceeds the budget.
```

The call is budgeted, metered, and audited by RiskKernel, then forwarded to LiteLLM,
which calls Gemini with the key it holds.

## What governs what — be honest about the split

| Concern | Owner |
|---|---|
| Cost / token / loop / time budgets (hard ceilings) | **RiskKernel** |
| Kill switch, crash-resume / checkpoints | **RiskKernel** |
| Human-in-the-loop approval gates | **RiskKernel** |
| Cost ledger, audit trail, OTel GenAI spans | **RiskKernel** |
| Routing to 100+ providers, provider failover, load balancing | **LiteLLM** |
| Holding the real provider API keys | **LiteLLM** |

RiskKernel meters cost from the **token usage LiteLLM passes back** in the response —
the same mechanism as a native call — so spend is metered for any provider LiteLLM
returns usage for. RiskKernel does **not** rely on LiteLLM's own spend tracking; it
prices the usage itself.

### Cost accuracy — the one caveat

RiskKernel prices a call by multiplying provider-reported tokens by a **per-model
rate from its own token→$ table** (see [`docs/BUDGETS.md`](BUDGETS.md)). The built-in
rates only cover the native families (`claude-*`, `gpt-*`). A long-tail model
RiskKernel doesn't have a rate for is metered at **$0** and recorded with
`priced: false`: its tokens still count toward the **token** budget, but it can't
count toward the **dollar** budget until you add a rate.

Close that gap with a `RISKKERNEL_PRICING_FILE` — a JSON file of model→rate overrides
(USD per 1M tokens), keyed by model name or prefix:

```json
{
  "gemini-1.5-pro": { "inputPerM": 1.25, "outputPerM": 5.0 },
  "command-r":      { "inputPerM": 0.5,  "outputPerM": 1.5 }
}
```

Use the model names your app sends (the `model_name` from LiteLLM's config — that's
what comes back in the response and gets priced). The daemon refuses to start on a
malformed pricing file, and logs how many overrides it loaded. See the pricing
section of [`docs/BUDGETS.md`](BUDGETS.md) for the full format and stability promise.

## Limitations, stated plainly

- **One OpenAI provider slot.** RiskKernel has a single "openai" provider, and its
  upstream is either the real OpenAI API or your LiteLLM proxy — not both at once.
  When you point `RISKKERNEL_OPENAI_BASE_URL` at LiteLLM, *all* "openai" routing
  (including `gpt-*` names) goes to LiteLLM. Expose OpenAI models through LiteLLM too
  if you need them. There's no per-model upstream routing inside RiskKernel beyond
  the prefix rule today; native `claude-*` continues to route to Anthropic
  independently of this.
- **No dollar budget without a rate.** Long-tail models need a pricing override to
  participate in the dollar budget (above). Token/loop/time budgets work regardless.
- **You run LiteLLM.** This adds a hop and a process to operate. For purely
  Anthropic/OpenAI/Ollama stacks, native providers need no LiteLLM at all.
- **Streaming** works end-to-end (LiteLLM is OpenAI-compatible SSE), and the call is
  metered from the stream's final usage chunk — same as a native streamed call.
