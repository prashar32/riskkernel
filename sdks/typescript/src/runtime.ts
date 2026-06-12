import { RiskKernel, type ClientOptions, type RunData, type Checkpoint } from "./client";
import { ApprovalTimeout } from "./errors";

/** Hard per-run limits. Any field omitted is unlimited for that dimension. */
export interface Budget {
  tokens?: number;
  dollars?: number;
  loops?: number;
  seconds?: number;
}

/** The resolution of an approval request. */
export interface Decision {
  approved: boolean;
  required: boolean;
  reason: string;
  by: string;
}

/** Where to point an LLM client so its calls are governed by a run. */
export interface ProxyConfig {
  baseUrl: string;
  headers: Record<string, string>;
}

export interface RuntimeOptions extends ClientOptions {
  /** Supply a pre-built client instead of base URL / token. */
  client?: RiskKernel;
  /** How often `approve()` polls a pending gate, in ms. Default 2000. */
  approvalPollIntervalMs?: number;
  /** Default ms to wait for an approval before throwing. Default: wait forever. */
  approvalTimeoutMs?: number;
}

/** A governed run bound to a daemon run id. */
export class Run {
  readonly id: string;

  constructor(
    private readonly client: RiskKernel,
    readonly data: RunData,
    private readonly pollMs: number,
    private readonly timeoutMs?: number,
  ) {
    this.id = data.id;
  }

  /** Register a loop iteration; throws {@link BudgetExceeded} if the loop/time budget is spent. */
  step(): Promise<number> {
    return this.client.beginStep(this.id);
  }

  checkpoint(name = "", payload: Record<string, unknown> = {}): Promise<void> {
    return this.client.checkpoint(this.id, name, payload);
  }

  latestCheckpoint(): Promise<Checkpoint | null> {
    return this.client.latestCheckpoint(this.id);
  }

  cancel(reason = ""): Promise<RunData> {
    return this.client.cancel(this.id, reason);
  }

  status(): Promise<RunData> {
    return this.client.getRun(this.id);
  }

  /**
   * Config for routing this run's model calls through the governing proxy: point
   * your LLM client's base URL here and send the header, so every call is metered,
   * priced, and budget-enforced under this run.
   */
  proxyConfig(): ProxyConfig {
    return {
      baseUrl: this.client.baseUrl + "/v1",
      headers: { "X-RiskKernel-Run-Id": this.id },
    };
  }

  /**
   * Request approval for a (possibly side-effecting) tool call, polling until a
   * human resolves it. Returns a {@link Decision}; throws {@link ApprovalTimeout}
   * if none arrives within the timeout.
   */
  async approve(
    tool: string,
    opts: {
      sideEffect?: string;
      arguments?: Record<string, unknown>;
      stepIndex?: number;
      pollIntervalMs?: number;
      timeoutMs?: number;
    } = {},
  ): Promise<Decision> {
    const res = await this.client.requestApproval(this.id, tool, opts.sideEffect ?? "", opts.arguments ?? {}, opts.stepIndex ?? 0);
    if (res.status === "approved") {
      return { approved: true, required: res.required ?? true, reason: "", by: "" };
    }
    const approvalId = res.id!;
    const interval = opts.pollIntervalMs ?? this.pollMs;
    const limit = opts.timeoutMs ?? this.timeoutMs;
    const deadline = limit !== undefined ? Date.now() + limit : undefined;
    for (;;) {
      const a = await this.client.getApproval(approvalId);
      if (a.status === "approved") return { approved: true, required: true, reason: a.reason ?? "", by: a.decidedBy ?? "" };
      if (a.status === "denied") return { approved: false, required: true, reason: a.reason ?? "", by: a.decidedBy ?? "" };
      if (deadline !== undefined && Date.now() > deadline) {
        throw new ApprovalTimeout(`no decision for approval ${approvalId} within ${limit}ms`);
      }
      await sleep(interval);
    }
  }
}

/** Entry point: holds a client and default approval-polling settings. */
export class Runtime {
  readonly client: RiskKernel;
  private readonly pollMs: number;
  private readonly timeoutMs?: number;

  constructor(opts: RuntimeOptions = {}) {
    this.client = opts.client ?? new RiskKernel(opts);
    this.pollMs = opts.approvalPollIntervalMs ?? 2000;
    this.timeoutMs = opts.approvalTimeoutMs;
  }

  /** Build a {@link Budget} (identity helper, for symmetry with the Python SDK). */
  budget(b: Budget): Budget {
    return b;
  }

  /**
   * Open a governed run, invoke `fn` with it, and cancel the run if `fn` throws
   * (unless `cancelOnError` is false). Mirrors the Python `with governed_run(...)`.
   */
  async governedRun<T>(
    opts: { name?: string; budget?: Budget; metadata?: Record<string, unknown>; cancelOnError?: boolean },
    fn: (run: Run) => Promise<T>,
  ): Promise<T> {
    const data = await this.client.createRun({
      name: opts.name,
      budget: toBudgetDict(opts.budget),
      metadata: opts.metadata,
    });
    const run = new Run(this.client, data, this.pollMs, this.timeoutMs);
    try {
      return await fn(run);
    } catch (e) {
      if (opts.cancelOnError !== false) {
        try {
          await run.cancel("error");
        } catch {
          /* best-effort */
        }
      }
      throw e;
    }
  }

  /**
   * Re-attach to an existing governed run by id — the resume path after a crash.
   *
   * Unlike {@link governedRun}, this neither creates a new run nor cancels on
   * error: the daemon reloads non-terminal runs on restart with the budget and
   * usage they had already spent, so enforcement continues without re-spending.
   * Read {@link Run.latestCheckpoint} to pick the work back up where it left off:
   *
   * ```ts
   * await rt.resumeRun(runId, async (run) => {
   *   const cp = await run.latestCheckpoint();
   *   const start = (cp?.payload?.cursor as number) ?? 0;
   *   for (let i = start; i < total; i++) {
   *     await run.step();                        // counts against the SAME budget
   *     // ... your agent's work ...
   *     await run.checkpoint("progress", { cursor: i + 1 });
   *   }
   * });
   * ```
   *
   * The run id is the only thing the agent must keep across a restart. Throws
   * {@link APIError} (404) if the run id is unknown.
   */
  async resumeRun<T>(runId: string, fn: (run: Run) => Promise<T>): Promise<T> {
    const data = await this.client.getRun(runId);
    const run = new Run(this.client, data, this.pollMs, this.timeoutMs);
    return fn(run);
  }
}

function toBudgetDict(b?: Budget): Record<string, number> | undefined {
  if (!b) return undefined;
  const out: Record<string, number> = {};
  if (b.tokens !== undefined) out.tokens = b.tokens;
  if (b.dollars !== undefined) out.dollars = b.dollars;
  if (b.loops !== undefined) out.loops = b.loops;
  if (b.seconds !== undefined) out.seconds = b.seconds;
  return Object.keys(out).length ? out : undefined;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
