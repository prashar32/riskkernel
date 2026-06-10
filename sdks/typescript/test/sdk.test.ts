import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createServer, type Server, type ServerResponse } from "node:http";
import { Runtime } from "../src/index";

// A tiny in-process mock of the daemon's /v1 API, so the SDK is exercised over
// real HTTP with no daemon, no keys.
const state = { steps: 0, haltAfter: Number.POSITIVE_INFINITY, cancelled: false };
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
      if (method === "POST" && url.endsWith("/cancel")) {
        state.cancelled = true;
        return send(res, 200, { id: "run-test", status: "cancelled" });
      }
      if (method === "GET" && url.startsWith("/v1/checkpoints/")) {
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
});
