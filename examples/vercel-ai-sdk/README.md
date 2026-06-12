# vercel-ai-sdk — stop a runaway Vercel AI SDK agent

Wrap a [Vercel AI SDK](https://ai-sdk.dev) model with RiskKernel's middleware and
the **deterministic governor caps the loop** — one governed step per model call,
hard-stopped at the loop budget. The halt propagates out of `generateText()` and
ends the loop. The kill comes from RiskKernel, not from the script.

**No API key, no model call.** This uses a tiny fake model so the loop enforcement
runs with nothing but `riskkernel serve`. (Add a real model — and the dollar/token
ceiling — by pointing a provider at the governing proxy; see [below](#add-the-dollar--token-ceiling-real-model).)

## Run it in 60 seconds

```bash
# 1. start the daemon — no key needed for this demo
docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest

# 2. in another terminal, install and run it
cd examples/vercel-ai-sdk
npm install
npm start
```

## What you'll see

```
▶ vercel-ai-sdk   loop budget = 6   (enforced by the Go governor)
  run id: 31ab03d4-e7e4-47ad-bb74-6402999ca4fd

  step  1 │ model call allowed by the governor
  step  2 │ model call allowed by the governor
  step  3 │ model call allowed by the governor
  step  4 │ model call allowed by the governor
  step  5 │ model call allowed by the governor
  step  6 │ model call allowed by the governor

🛑 RiskKernel halted the agent — reason: loop_budget_exceeded
   the loop never ran past its budget; the kill was deterministic.
```

## How it works

```ts
import { generateText, wrapLanguageModel } from "ai";
import { Runtime } from "@riskkernel/sdk";
import { governMiddleware } from "@riskkernel/sdk/vercel";

await rt.governedRun({ budget: { loops: 6 } }, async (run) => {
  const model = wrapLanguageModel({ model: yourModel, middleware: governMiddleware(run) });
  // every generateText/streamText on `model` now ticks one governed step
  await generateText({ model, prompt });   // throws BudgetExceeded when the budget is spent
});
```

`governMiddleware(run)` is an AI SDK
[language-model middleware](https://ai-sdk.dev/docs/ai-sdk-core/middleware): its
`wrapGenerate` / `wrapStream` hooks call `run.step()` before each model call, so the
governor's loop and time budgets are enforced and a halt surfaces as
`BudgetExceeded` instead of being swallowed. The daemon decides; the adapter carries
no governance logic.

## Add the dollar / token ceiling (real model)

The middleware enforces the *loop* and *time* budgets — the outer-loop count the
provider can't see. For the *dollar* and *token* budgets, route the model through
the governing proxy so every call is metered and priced. One config change:

```ts
import { createOpenAI } from "@ai-sdk/openai";

const { baseUrl, headers } = run.proxyConfig();
const openai = createOpenAI({ baseURL: baseUrl, headers });          // BYO key at the daemon
const model = wrapLanguageModel({
  model: openai("gpt-4o-mini"),
  middleware: governMiddleware(run),                                 // loop/time
});
// dollars/tokens metered by the proxy · loops/time by the middleware
```

## Supported versions

Pinned and tested against **Vercel AI SDK v5** (`ai@^5`, `@ai-sdk/provider@^2`). The
adapter ships in the SDK as `@riskkernel/sdk/vercel`; `@ai-sdk/provider` is an
optional peer, needed only if you import the adapter.
