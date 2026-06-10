/**
 * RiskKernel TypeScript SDK — Surface 2 (deep control).
 *
 * A thin client over the self-hosted RiskKernel daemon. The Go core makes every
 * deterministic decision (budgets, halts, approval policy); this package just
 * makes governed runs ergonomic from Node/TypeScript. No runtime dependencies.
 *
 * ```ts
 * import { Runtime } from "@riskkernel/sdk";
 *
 * const rt = new Runtime({ baseUrl: "http://localhost:7070" });
 * await rt.governedRun(
 *   { name: "research", budget: { dollars: 1.0, loops: 20 } },
 *   async (run) => {
 *     const { baseUrl, headers } = run.proxyConfig(); // route your LLM client here
 *     for (let i = 0; i < 100; i++) {
 *       await run.step(); // throws BudgetExceeded when loops/time run out
 *     }
 *   },
 * );
 * ```
 */
export { RiskKernel } from "./client";
export type { ClientOptions, RunData, Checkpoint, ApprovalData } from "./client";
export { Runtime, Run } from "./runtime";
export type { Budget, Decision, ProxyConfig, RuntimeOptions } from "./runtime";
export { RiskKernelError, APIError, BudgetExceeded, ApprovalDenied, ApprovalTimeout } from "./errors";
