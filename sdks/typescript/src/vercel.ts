/**
 * Vercel AI SDK adapter — govern a Node agent with ~no code change.
 *
 * The JS analog of the Python LangChain / OpenAI-Agents adapters: a
 * [language-model middleware](https://ai-sdk.dev/docs/ai-sdk-core/middleware)
 * that ticks one governed step per model call, so a run's loop/time budget is
 * enforced and a halt surfaces as {@link BudgetExceeded} — propagating out of
 * `generateText` / `streamText` instead of being swallowed. The daemon decides;
 * this adapter carries no governance logic.
 *
 * Pair it with the governing proxy for token/cost metering the middleware can't
 * see — point your provider's `baseURL` at `run.proxyConfig()`:
 *
 * ```ts
 * import { createOpenAI } from "@ai-sdk/openai";
 * import { generateText, wrapLanguageModel } from "ai";
 * import { Runtime } from "@riskkernel/sdk";
 * import { governMiddleware } from "@riskkernel/sdk/vercel";
 *
 * const rt = new Runtime();
 * await rt.governedRun({ name: "research", budget: { loops: 20, dollars: 1 } }, async (run) => {
 *   const { baseUrl, headers } = run.proxyConfig();
 *   const openai = createOpenAI({ baseURL: baseUrl, headers }); // cost metered by the proxy
 *   const model = wrapLanguageModel({ model: openai("gpt-4o-mini"), middleware: governMiddleware(run) });
 *   for (;;) {
 *     const { text } = await generateText({ model, prompt }); // throws BudgetExceeded when budget runs out
 *     // ... your agent's work ...
 *   }
 * });
 * ```
 *
 * Tested against `@ai-sdk/provider` v2 (Vercel AI SDK v5). `@ai-sdk/provider` is
 * an optional peer — only needed if you import this adapter. The type is used at
 * compile time only (`import type`), so the built adapter has no runtime deps.
 */
import type { LanguageModelV2Middleware } from "@ai-sdk/provider";
import type { Run } from "./runtime";

/**
 * Build an AI SDK middleware that binds model calls to a governed run. Each
 * `generateText` / `streamText` ticks one {@link Run.step} (loop/time budget);
 * when the budget is spent the daemon halts the run and the call throws
 * {@link BudgetExceeded}. Wrap any model with it via `wrapLanguageModel`.
 */
export function governMiddleware(run: Run): LanguageModelV2Middleware {
  return {
    middlewareVersion: "v2",
    // One model call == one governed step (mirrors the Python on_llm_start hook).
    async wrapGenerate({ doGenerate }) {
      await run.step();
      return doGenerate();
    },
    async wrapStream({ doStream }) {
      await run.step();
      return doStream();
    },
  };
}
