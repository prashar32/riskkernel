/** Base class for all RiskKernel SDK errors. */
export class RiskKernelError extends Error {}

/** The daemon returned an unexpected (non-2xx) response. */
export class APIError extends RiskKernelError {
  constructor(
    public readonly status: number,
    public readonly code = "",
    public readonly apiMessage = "",
  ) {
    super(`riskkernel API error ${status} ${code}: ${apiMessage}`);
    this.name = "APIError";
  }
}

/**
 * A governed run hit one of its hard budgets — the deterministic governor halted
 * it. `reason` is the machine-readable HaltReason, e.g. `token_budget_exceeded`,
 * `dollar_budget_exceeded`, or `loop_budget_exceeded`.
 */
export class BudgetExceeded extends RiskKernelError {
  constructor(
    public readonly reason: string,
    message = "",
  ) {
    super(message || `run halted: ${reason}`);
    this.name = "BudgetExceeded";
  }
}

/** A human denied a side-effecting tool call gated by the approval gate. */
export class ApprovalDenied extends RiskKernelError {
  constructor(
    public readonly tool: string,
    public readonly reason = "",
  ) {
    super(`approval denied for ${tool}${reason ? ": " + reason : ""}`);
    this.name = "ApprovalDenied";
  }
}

/** No human resolved a pending approval within the configured timeout. */
export class ApprovalTimeout extends RiskKernelError {
  constructor(message: string) {
    super(message);
    this.name = "ApprovalTimeout";
  }
}
