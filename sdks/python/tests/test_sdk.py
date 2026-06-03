"""SDK tests against a stdlib stub daemon — no Go binary, no third-party deps."""

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import riskkernel as rk
from riskkernel.errors import ApprovalDenied, BudgetExceeded


def _has_langchain() -> bool:
    try:
        import langchain_core  # noqa: F401

        return True
    except Exception:
        return False


class _State:
    def __init__(self):
        self.steps = 0
        self.loop_budget = 2
        self.approval_polls = 0
        self.last_checkpoint = None


STATE = _State()


class StubHandler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # silence
        pass

    def _send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read(self):
        n = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(n) if n else b""
        return json.loads(raw) if raw else {}

    def do_GET(self):
        p = self.path
        if p.startswith("/v1/memory/facts"):
            return self._send(200, [{"namespace": "dev", "key": "db", "value": "sqlite"}])
        if p.startswith("/v1/memory/entry"):
            return self._send(200, {"name": "style.md", "title": "Style", "content": "use tabs"})
        if p.startswith("/v1/memory"):
            return self._send(200, [{"name": "style.md", "title": "Style", "format": "markdown"}])
        if p.startswith("/v1/approvals/"):
            STATE.approval_polls += 1
            status = "approved" if STATE.approval_polls >= 2 else "pending"
            return self._send(200, {"id": "ap-1", "status": status, "decidedBy": "tester"})
        if p.startswith("/v1/checkpoints/"):
            return self._send(200, {"runId": "run-1", "stepIndex": 1,
                                    "payload": STATE.last_checkpoint or {}})
        return self._send(404, {"code": "not_found", "message": "no"})

    def do_POST(self):
        p = self.path
        body = self._read()
        if p == "/v1/runs":
            return self._send(201, {"id": "run-1", "status": "running",
                                    "budget": body.get("budget", {}),
                                    "usage": {"tokens": 0, "loops": 0}})
        if p == "/v1/runs/run-1/steps":
            STATE.steps += 1
            if STATE.steps > STATE.loop_budget:
                return self._send(402, {"code": "loop_budget_exceeded",
                                        "message": "run halted: loop_budget_exceeded"})
            return self._send(200, {"stepIndex": STATE.steps})
        if p == "/v1/runs/run-1/checkpoints":
            STATE.last_checkpoint = body.get("payload")
            return self._send(201, {"ok": True})
        if p == "/v1/runs/run-1/cancel":
            return self._send(200, {"id": "run-1", "status": "cancelled"})
        if p == "/v1/runs/run-1/approvals":
            if not body.get("sideEffect"):
                return self._send(200, {"status": "approved", "required": False})
            return self._send(201, {"id": "ap-1", "status": "pending"})
        if p == "/v1/runs/run-1/approve":
            return self._send(200, {"id": "run-1", "status": "running"})
        return self._send(404, {"code": "not_found", "message": "no"})

    def do_PUT(self):
        if self.path == "/v1/memory/facts":
            self._read()
            return self._send(201, {"namespace": "dev", "key": "db", "value": "sqlite"})
        return self._send(404, {"code": "not_found", "message": "no"})


class SDKTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), StubHandler)
        cls.port = cls.server.server_address[1]
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.base = f"http://127.0.0.1:{cls.port}"

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()

    def setUp(self):
        global STATE
        STATE = _State()  # the handler reads this module global
        self.rt = rk.Runtime(base_url=self.base, approval_poll_interval=0.01)

    def test_governed_run_and_step_budget(self):
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=2)) as run:
            self.assertEqual(run.id, "run-1")
            self.assertEqual(run.step(), 1)
            self.assertEqual(run.step(), 2)
            with self.assertRaises(BudgetExceeded) as cm:
                run.step()
            self.assertEqual(cm.exception.reason, "loop_budget_exceeded")

    def test_checkpoint_roundtrip(self):
        with self.rt.governed_run(name="t") as run:
            run.checkpoint("after", {"cursor": 7})
            cp = run.latest_checkpoint()
            self.assertEqual(cp["payload"]["cursor"], 7)

    def test_budget_to_dict(self):
        b = self.rt.budget(tokens=100, dollars=1.5)
        self.assertEqual(b.to_dict(), {"tokens": 100, "dollars": 1.5})

    def test_approval_not_required(self):
        with self.rt.governed_run(name="t") as run:
            d = run.approve("mcp://fs", side_effect="")  # read-only
            self.assertTrue(d.approved)
            self.assertFalse(d.required)

    def test_approval_pending_then_approved(self):
        with self.rt.governed_run(name="t") as run:
            d = run.approve("mcp://shell", side_effect="exec", arguments={"cmd": "ls"})
            self.assertTrue(d.approved)  # stub flips to approved on 2nd poll

    def test_governed_tool_denied(self):
        # Make the stub deny: flip approval to "denied" by overriding poll result.
        with self.rt.governed_run(name="t") as run:
            # Monkeypatch the client to return a denied approval.
            run._client.get_approval = lambda _id: {"id": "ap-1", "status": "denied", "reason": "no"}

            @rk.governed_tool(side_effect="write", tool="danger")
            def danger():
                return "ran"

            with self.assertRaises(ApprovalDenied):
                danger()

    def test_cancel(self):
        with self.rt.governed_run(name="t") as run:
            out = run.cancel("done")
            self.assertEqual(out["status"], "cancelled")

    def test_memory(self):
        c = self.rt.client
        entries = c.list_memory(namespace="dev")
        self.assertEqual(entries[0]["title"], "Style")
        entry = c.read_memory("style.md", namespace="dev")
        self.assertEqual(entry["content"], "use tabs")
        facts = c.list_facts(namespace="dev")
        self.assertEqual(facts[0]["key"], "db")
        out = c.put_fact("dev", "db", "sqlite")
        self.assertEqual(out["value"], "sqlite")

    def test_proxy_config(self):
        with self.rt.governed_run(name="t") as run:
            cfg = run.proxy_config()
            self.assertTrue(cfg["base_url"].endswith("/v1"))
            self.assertEqual(cfg["headers"]["X-RiskKernel-Run-Id"], "run-1")

    # --- LangChain adapter ---

    def test_langchain_handler_enforces_loop_budget(self):
        from riskkernel.adapters.langchain import RiskKernelCallbackHandler

        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=2)) as run:
            h = RiskKernelCallbackHandler(run)
            # Must stay True, or LangChain silently swallows the halt and the chain
            # keeps spending past budget (see the handler comment + integration test).
            self.assertTrue(h.raise_error)
            h.on_chat_model_start({}, [])           # step 1
            h.on_chat_model_start({}, [])           # step 2
            with self.assertRaises(BudgetExceeded) as cm:
                h.on_chat_model_start({}, [])       # step 3 → over the loop budget
            self.assertEqual(cm.exception.reason, "loop_budget_exceeded")

    def test_langchain_handler_gates_denied_tool(self):
        from riskkernel.adapters.langchain import RiskKernelCallbackHandler

        with self.rt.governed_run(name="t") as run:
            run._client.get_approval = lambda _id: {
                "id": "ap-1", "status": "denied", "reason": "no"}
            h = RiskKernelCallbackHandler(run, gate_tools=True)
            with self.assertRaises(ApprovalDenied):
                h.on_tool_start({"name": "deploy"}, "ship it")

    @unittest.skipUnless(_has_langchain(), "langchain-core not installed")
    def test_langchain_integration_stops_runaway_loop(self):
        # The real proof: a runaway LangChain loop must actually stop. on_llm_start
        # raises BudgetExceeded at the cap, and raise_error=True lets it propagate
        # out of llm.invoke() — without that flag LangChain would swallow it.
        from langchain_core.language_models.fake import FakeListLLM

        from riskkernel.adapters.langchain import RiskKernelCallbackHandler

        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=2)) as run:
            h = RiskKernelCallbackHandler(run)
            llm = FakeListLLM(responses=["keep going"] * 10)
            calls = 0
            with self.assertRaises(BudgetExceeded):
                while True:                         # a deliberately runaway loop
                    llm.invoke("step", config={"callbacks": [h]})
                    calls += 1
            self.assertEqual(calls, 2)              # 2 calls allowed; the 3rd halted


if __name__ == "__main__":
    unittest.main()
