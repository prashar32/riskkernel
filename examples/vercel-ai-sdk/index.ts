/**
 * vercel-ai-sdk — stop a runaway Vercel AI SDK agent.
 *
 * Wrap a model with RiskKernel's middleware and the deterministic governor caps
 * the loop: one governed step per model call, hard-stopped at the loop budget.
 * The halt propagates out of `generateText()` and ends the loop — the kill comes
 * from RiskKernel, not from this script.
 *
 * No API key, no model call: this uses a tiny fake model so the loop enforcement
 * runs with nothing but `riskkernel serve`. Add a real model — and the dollar/token
 * ceiling — by pointing a provider at the governing proxy (see the README).
 */
import { generateText, wrapLanguageModel } from "ai";
import type { LanguageModelV2 } from "@ai-sdk/provider";
import { Runtime, BudgetExceeded } from "@riskkernel/sdk";
import { governMiddleware } from "@riskkernel/sdk/vercel";

// A key-free fake model (the AI SDK analog of LangChain's FakeListLLM). Only
// doGenerate is exercised here; doStream is present to satisfy the interface.
const fakeModel: LanguageModelV2 = {
  specificationVersion: "v2",
  provider: "fake",
  modelId: "fake-echo",
  supportedUrls: {},
  async doGenerate() {
    return {
      content: [{ type: "text", text: "ok" }],
      finishReason: "stop",
      usage: { inputTokens: 5, outputTokens: 1, totalTokens: 6 },
      warnings: [],
    };
  },
  async doStream() {
    throw new Error("streaming is not used in this demo");
  },
};

const LOOP_BUDGET = 6;

// BudgetExceeded is thrown inside the middleware; surface it even if a wrapper
// (e.g. the AI SDK) nests it under `.cause`.
function asBudgetExceeded(err: unknown): BudgetExceeded | null {
  let e: unknown = err;
  for (let i = 0; i < 5 && e; i++) {
    if (e instanceof BudgetExceeded) return e;
    e = (e as { cause?: unknown }).cause;
  }
  return null;
}

async function main(): Promise<void> {
  const rt = new Runtime({ baseUrl: process.env.RISKKERNEL_BASE_URL ?? "http://localhost:7070" });

  await rt.governedRun(
    { name: "vercel-ai-sdk", budget: { loops: LOOP_BUDGET }, cancelOnError: false },
    async (run) => {
      const model = wrapLanguageModel({ model: fakeModel, middleware: governMiddleware(run) });

      console.log(`▶ vercel-ai-sdk   loop budget = ${LOOP_BUDGET}   (enforced by the Go governor)`);
      console.log(`  run id: ${run.id}\n`);

      try {
        for (let i = 1; ; i++) {
          // Each call ticks one governed step; the governor halts the (LOOP_BUDGET+1)th.
          await generateText({ model, prompt: "say ok", maxRetries: 0 });
          console.log(`  step ${String(i).padStart(2)} │ model call allowed by the governor`);
        }
      } catch (err) {
        const halt = asBudgetExceeded(err);
        if (!halt) throw err;
        console.log(`\n🛑 RiskKernel halted the agent — reason: ${halt.reason}`);
        console.log("   the loop never ran past its budget; the kill was deterministic.");
      }
    },
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
