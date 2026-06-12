# @riskkernel/sdk

Thin TypeScript client for the [RiskKernel](https://github.com/prashar32/riskkernel)
reliability runtime — **Surface 2** (deep control). The Go daemon makes every
deterministic decision (budgets, halts, approval policy); this package just makes
governed runs ergonomic from Node/TypeScript. **No runtime dependencies** — it uses
the global `fetch` (Node 20+), the same stdlib-only ethos as the Python SDK.

> **Status:** core client — run control, budgets, crash-resume (`resumeRun`), the
> governing proxy, and approval gates. Framework adapters (Vercel AI SDK) and npm
> publishing are tracked in the repo issues (**#81–#82**) — contributions welcome.

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

## Resume after a crash

The daemon reloads non-terminal runs on restart with the budget and usage they had
already spent, so a `SIGKILL`'d run keeps enforcing without re-spending. Reattach to
it by id with `resumeRun` and pick your work back up from the last checkpoint:

```ts
await rt.resumeRun(runId, async (run) => {     // attaches; never creates or cancels
  const cp = await run.latestCheckpoint();     // the state you saved before the crash
  const start = (cp?.payload?.cursor as number) ?? 0;
  for (let i = start; i < total; i++) {        // skip the steps you already paid for
    await run.step();                          // counts against the SAME budget
    // ... your work ...
    await run.checkpoint("step", { cursor: i + 1 });
  }
});
```

The run resumes against whatever budget it had left, so it can't overspend by
restarting — `run.step()` still throws `BudgetExceeded` at the original ceiling. The
run id is the only thing to keep across a restart (a file, your job queue, a DB row);
see [`docs/RESUME.md`](../../docs/RESUME.md) for the full model.

## API

- `new Runtime(opts)` — `{ baseUrl, token, approvalPollIntervalMs, approvalTimeoutMs }`.
- `rt.governedRun({ name?, budget?, metadata?, cancelOnError? }, async (run) => …)`.
- `rt.resumeRun(runId, async (run) => …)` — re-attach to an existing run after a crash.
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
