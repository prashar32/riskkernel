import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createServer, type Server, type ServerResponse } from "node:http";
import { Runtime } from "../src/index";
import { governMiddleware } from "../src/vercel";

// A tiny in-process mock of the daemon's /v1 API, so the SDK is exercised over
// real HTTP with no daemon, no keys.
const state = {
  steps: 0,
  haltAfter: Number.POSITIVE_INFINITY,
  cancelled: false,
  checkpoint: null as { name: string; payload: Record<string, unknown> } | null,
};
let server: Server;
let baseUrl = "";

function send(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

beforeAll(async () => {
  server = createServer((req, res) => {
    const url = req.url ?? "";
    const method = req.method ?? "GET";
    let buf = "";
    req.on("data", (c) => (buf += c));
    req.on("end", () => {
      if (method === "POST" && url === "/v1/runs") return send(res, 200, { id: "run-test", status: "running" });
      if (method === "POST" && url.endsWith("/steps")) {
        if (state.steps >= state.haltAfter) {
          return send(res, 402, { code: "dollar_budget_exceeded", message: "run halted: dollar_budget_exceeded" });
        }
        return send(res, 200, { stepIndex: state.steps++ });
      }
      if (method === "POST" && url.endsWith("/checkpoints")) {
        const body = buf ? JSON.parse(buf) : {};
        state.checkpoint = { name: body.name ?? "", payload: body.payload ?? {} };
        return send(res, 200, {});
      }
      if (method === "POST" && url.endsWith("/cancel")) {
        state.cancelled = true;
        return send(res, 200, { id: "run-test", status: "cancelled" });
      }
      if (method === "GET" && url.startsWith("/v1/runs/")) {
        // Resume path: GET /v1/runs/{id} reloads a run with the usage it already
        // spent. Unknown ids 404 (mirrors the daemon).
        if (url === "/v1/runs/run-test") {
          return send(res, 200, { id: "run-test", status: "running", usage: { loops: state.steps } });
        }
        return send(res, 404, { code: "not_found", message: "unknown run" });
      }
      if (method === "GET" && url.startsWith("/v1/checkpoints/")) {
        if (state.checkpoint) return send(res, 200, state.checkpoint);
        return send(res, 404, { code: "not_found", message: "no checkpoint" });
      }
      return send(res, 404, { code: "not_found", message: "unhandled" });
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", () => r()));
  const addr = server.address();
  baseUrl = `http://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}`;
});

afterAll(() => new Promise<void>((r) => server.close(() => r())));

function reset(): void {
  state.steps = 0;
  state.haltAfter = Number.POSITIVE_INFINITY;
  state.cancelled = false;
  state.checkpoint = null;
}

describe("Runtime", () => {
  it("governedRun opens a run, runs the body, and returns its value", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    const result = await rt.governedRun({ name: "t", budget: { loops: 5 } }, async (run) => {
      expect(run.id).toBe("run-test");
      await run.step();
      await run.step();
      return "done";
    });
    expect(result).toBe("done");
    expect(state.steps).toBe(2);
    expect(state.cancelled).toBe(false);
  });

  it("step throws BudgetExceeded on a 402 halt, carrying the reason", async () => {
    reset();
    state.haltAfter = 2;
    const rt = new Runtime({ baseUrl });
    await expect(
      rt.governedRun({ budget: { loops: 2 }, cancelOnError: false }, async (run) => {
        for (let i = 0; i < 10; i++) await run.step();
      }),
    ).rejects.toMatchObject({ name: "BudgetExceeded", reason: "dollar_budget_exceeded" });
  });

  it("governedRun cancels the run when the body throws", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    await expect(rt.governedRun({}, async () => { throw new Error("boom"); })).rejects.toThrow("boom");
    expect(state.cancelled).toBe(true);
  });

  it("proxyConfig points at /v1 with the run-id header", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    await rt.governedRun({}, async (run) => {
      const cfg = run.proxyConfig();
      expect(cfg.baseUrl).toBe(baseUrl + "/v1");
      expect(cfg.headers["X-RiskKernel-Run-Id"]).toBe("run-test");
    });
  });

  it("latestCheckpoint returns null on a 404", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    await rt.governedRun({}, async (run) => {
      expect(await run.latestCheckpoint()).toBeNull();
    });
  });

  it("resumeRun re-attaches to a crashed run and continues from its checkpoint without re-spending", async () => {
    reset();
    // Simulate a run that crashed after 3 paid steps, having checkpointed cursor 3.
    state.steps = 3;
    state.checkpoint = { name: "progress", payload: { cursor: 3 } };
    const rt = new Runtime({ baseUrl });

    const seen: number[] = [];
    await rt.resumeRun("run-test", async (run) => {
      expect(run.id).toBe("run-test");
      const cp = await run.latestCheckpoint();
      const start = (cp?.payload?.cursor as number) ?? 0;
      expect(start).toBe(3); // skip the work already paid for
      for (let i = start; i < 6; i++) {
        seen.push(await run.step()); // steps count against the SAME budget — no reset
        await run.checkpoint("progress", { cursor: i + 1 });
      }
    });

    // The step counter continued at 3 rather than restarting at 0.
    expect(seen).toEqual([3, 4, 5]);
    expect(state.steps).toBe(6);
    expect(state.cancelled).toBe(false);
  });

  it("resumeRun does NOT cancel the run when the body throws", async () => {
    reset();
    state.steps = 1;
    const rt = new Runtime({ baseUrl });
    await expect(
      rt.resumeRun("run-test", async () => {
        throw new Error("boom");
      }),
    ).rejects.toThrow("boom");
    expect(state.cancelled).toBe(false); // resume must never cancel a run it didn't start
  });

  it("resumeRun surfaces APIError(404) for an unknown run id", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    await expect(
      rt.resumeRun("does-not-exist", async () => "unreachable"),
    ).rejects.toMatchObject({ name: "APIError", status: 404 });
  });
});

describe("governMiddleware (Vercel AI SDK)", () => {
  it("ticks one governed step per model call and forwards the result", async () => {
    reset();
    const rt = new Runtime({ baseUrl });
    await rt.governedRun({ budget: { loops: 5 } }, async (run) => {
      const mw = governMiddleware(run);
      // Call the middleware hooks the way wrapLanguageModel would (params/model
      // are unused by the adapter; stub them).
      const gen = await (mw.wrapGenerate as any)({ doGenerate: async () => "GENERATED" });
      expect(gen).toBe("GENERATED");
      const str = await (mw.wrapStream as any)({ doStream: async () => "STREAMED" });
      expect(str).toBe("STREAMED");
    });
    expect(state.steps).toBe(2); // two model calls == two steps
  });

  it("surfaces BudgetExceeded out of a generate call when the loop budget is spent", async () => {
    reset();
    state.haltAfter = 1; // the daemon halts the 2nd step
    const rt = new Runtime({ baseUrl });
    await expect(
      rt.governedRun({ budget: { loops: 1 }, cancelOnError: false }, async (run) => {
        const mw = governMiddleware(run);
        const doGenerate = async () => "GENERATED";
        await (mw.wrapGenerate as any)({ doGenerate }); // 1st: ok
        await (mw.wrapGenerate as any)({ doGenerate }); // 2nd: halted before the model runs
      }),
    ).rejects.toMatchObject({ name: "BudgetExceeded", reason: "dollar_budget_exceeded" });
  });

  it("does not call the model when the step is rejected", async () => {
    reset();
    state.haltAfter = 0; // halt immediately
    const rt = new Runtime({ baseUrl });
    let modelCalled = false;
    await rt
      .governedRun({ cancelOnError: false }, async (run) => {
        const mw = governMiddleware(run);
        await (mw.wrapGenerate as any)({
          doGenerate: async () => {
            modelCalled = true;
            return "GENERATED";
          },
        });
      })
      .catch(() => {});
    expect(modelCalled).toBe(false); // step() throws first — the model never runs, so no spend
  });
});
