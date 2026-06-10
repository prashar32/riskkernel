# @riskkernel/sdk

Thin TypeScript client for the [RiskKernel](https://github.com/prashar32/riskkernel)
reliability runtime — **Surface 2** (deep control). The Go daemon makes every
deterministic decision (budgets, halts, approval policy); this package just makes
governed runs ergonomic from Node/TypeScript. **No runtime dependencies** — it uses
the global `fetch` (Node 18+), the same stdlib-only ethos as the Python SDK.

> **Status:** core client — run control, budgets, the governing proxy, and approval
> gates. Crash-resume (`resumeRun`), framework adapters (Vercel AI SDK), and npm
> publishing are tracked in the repo issues (**#80–#82**) — contributions welcome.

## Use

```ts
import { Runtime, BudgetExceeded } from "@riskkernel/sdk";

const rt = new Runtime({ baseUrl: "http://localhost:7070" });

await rt.governedRun(
  { name: "research", budget: { dollars: 1.0, loops: 20, seconds: 600 } },
  async (run) => {
    // Route your LLM client through the governing proxy — one config change:
    const { baseUrl, headers } = run.proxyConfig();
    //   new OpenAI({ baseURL: baseUrl, defaultHeaders: headers })

    for (let i = 0; i < 100; i++) {
      await run.step();                       // throws BudgetExceeded when loops/time run out
      await run.checkpoint("step", { cursor: i });
      // ... your agent's work ...
    }
  },
);
```

A budget halt surfaces as `BudgetExceeded` (`reason` is the machine-readable
HaltReason, e.g. `dollar_budget_exceeded`). The run is cancelled automatically if
the body throws — pass `cancelOnError: false` to opt out.

## API

- `new Runtime(opts)` — `{ baseUrl, token, approvalPollIntervalMs, approvalTimeoutMs }`.
- `rt.governedRun({ name?, budget?, metadata?, cancelOnError? }, async (run) => …)`.
- `run.step()` · `run.checkpoint(name, payload)` · `run.latestCheckpoint()` ·
  `run.cancel(reason)` · `run.status()` · `run.proxyConfig()` · `run.approve(tool, opts)`.
- `RiskKernel` — the low-level `/v1` client, for manual control.
- Errors: `BudgetExceeded`, `ApprovalDenied`, `ApprovalTimeout`, `APIError`.

The `/v1` contract is [`api/v1/openapi.yaml`](../../api/v1/openapi.yaml); the
governance principle is the same as every surface — **the LLM proposes, the
deterministic Go core disposes.**

## Develop

```bash
npm install
npm run typecheck
npm test            # vitest against an in-process mock daemon — no daemon, no keys
npm run build       # tsup → dist (ESM + CJS + .d.ts)
```
