import { APIError, BudgetExceeded } from "./errors";

export interface ClientOptions {
  /** Daemon base URL. Default `http://localhost:7070`. */
  baseUrl?: string;
  /** Bearer token, if the daemon has `RISKKERNEL_API_TOKEN` set. */
  token?: string;
  /** Per-request timeout in ms. Default 30000. */
  timeoutMs?: number;
}

/**
 * Thin HTTP client for the daemon's `/v1` API. It carries NO governance logic —
 * the Go daemon makes every deterministic decision (budgets, halts, approval
 * policy). This client relays calls and surfaces the daemon's verdicts: a 402
 * becomes {@link BudgetExceeded}. No runtime dependencies — uses global `fetch`
 * (Node 18+).
 */
export class RiskKernel {
  readonly baseUrl: string;
  private readonly token?: string;
  private readonly timeoutMs: number;

  constructor(opts: ClientOptions = {}) {
    this.baseUrl = (opts.baseUrl ?? "http://localhost:7070").replace(/\/+$/, "");
    this.token = opts.token;
    this.timeoutMs = opts.timeoutMs ?? 30_000;
  }

  private async request<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (this.token) headers["Authorization"] = "Bearer " + this.token;

    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
    let resp: Response;
    try {
      resp = await fetch(this.baseUrl + path, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: ctrl.signal,
      });
    } catch (e) {
      throw new APIError(0, "connection_error", `cannot reach daemon at ${this.baseUrl}: ${(e as Error).message}`);
    } finally {
      clearTimeout(timer);
    }

    const text = await resp.text();
    const payload = text ? safeJson(text) : {};
    if (!resp.ok) {
      const code = payload?.code ?? "";
      const message = payload?.message ?? resp.statusText;
      if (resp.status === 402) throw new BudgetExceeded(code, message); // the governor halted the run
      throw new APIError(resp.status, code, message);
    }
    return (payload ?? {}) as T;
  }

  // --- runs ---

  createRun(input: { name?: string; budget?: Record<string, number>; metadata?: Record<string, unknown> } = {}): Promise<RunData> {
    const body: Record<string, unknown> = {};
    if (input.name) body.name = input.name;
    if (input.budget) body.budget = input.budget;
    if (input.metadata) body.metadata = input.metadata;
    return this.request<RunData>("POST", "/v1/runs", body);
  }

  getRun(runId: string): Promise<RunData> {
    return this.request<RunData>("GET", `/v1/runs/${enc(runId)}`);
  }

  /** Register a loop iteration. Throws {@link BudgetExceeded} (402) if the loop or time budget is spent. */
  async beginStep(runId: string): Promise<number> {
    const out = await this.request<{ stepIndex?: number }>("POST", `/v1/runs/${enc(runId)}/steps`, {});
    return Number(out?.stepIndex ?? 0);
  }

  checkpoint(runId: string, name = "", payload: Record<string, unknown> = {}): Promise<void> {
    return this.request<void>("POST", `/v1/runs/${enc(runId)}/checkpoints`, { name, payload });
  }

  async latestCheckpoint(runId: string): Promise<Checkpoint | null> {
    try {
      return await this.request<Checkpoint>("GET", `/v1/checkpoints/${enc(runId)}`);
    } catch (e) {
      if (e instanceof APIError && e.status === 404) return null;
      throw e;
    }
  }

  cancel(runId: string, reason = ""): Promise<RunData> {
    return this.request<RunData>("POST", `/v1/runs/${enc(runId)}/cancel`, { reason });
  }

  // --- approvals ---

  requestApproval(runId: string, tool: string, sideEffect = "", args: Record<string, unknown> = {}, stepIndex = 0): Promise<ApprovalData> {
    return this.request<ApprovalData>("POST", `/v1/runs/${enc(runId)}/approvals`, {
      tool,
      sideEffect,
      arguments: args,
      stepIndex,
    });
  }

  getApproval(approvalId: string): Promise<ApprovalData> {
    return this.request<ApprovalData>("GET", `/v1/approvals/${enc(approvalId)}`);
  }

  resolveApproval(runId: string, approvalId: string, approve: boolean, reason = "", decidedBy = "sdk"): Promise<ApprovalData> {
    return this.request<ApprovalData>("POST", `/v1/runs/${enc(runId)}/approve`, {
      approvalId,
      decision: approve ? "approve" : "deny",
      reason,
      decidedBy,
    });
  }
}

export interface RunData {
  id: string;
  status?: string;
  usage?: { tokens?: number; dollars?: number; loops?: number; elapsedSeconds?: number };
  [k: string]: unknown;
}

export interface Checkpoint {
  name?: string;
  payload?: Record<string, unknown>;
  [k: string]: unknown;
}

export interface ApprovalData {
  id?: string;
  status?: "approved" | "pending" | "denied";
  required?: boolean;
  reason?: string;
  decidedBy?: string;
  [k: string]: unknown;
}

function enc(s: string): string {
  return encodeURIComponent(s);
}

function safeJson(s: string): { code?: string; message?: string; [k: string]: unknown } {
  try {
    return JSON.parse(s);
  } catch {
    return {};
  }
}
