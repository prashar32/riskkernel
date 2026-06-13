"""CrewAI adapter tests against a stdlib stub daemon — no Go binary, no crewai
needed for the unit tests (crewai is lazily imported and never required to import
or exercise the adapter; the real integration test is skipped unless it's present).
"""

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import riskkernel as rk
from riskkernel.adapters.crewai import RiskKernelStepCallback
from riskkernel.errors import ApprovalDenied, BudgetExceeded


def _has_crewai() -> bool:
    try:
        import crewai  # noqa: F401

        return True
    except Exception:
        return False


class _State:
    def __init__(self):
        self.steps = 0
        self.loop_budget = 2
        self.approval_polls = 0
        self.approvals_requested = 0


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
        if p == "/v1/runs/run-1":
            return self._send(200, {"id": "run-1", "name": "t", "status": "running",
                                    "usage": {"tokens": 0, "loops": STATE.steps}})
        if p.startswith("/v1/approvals/"):
            STATE.approval_polls += 1
            status = "approved" if STATE.approval_polls >= 2 else "pending"
            return self._send(200, {"id": "ap-1", "status": status, "decidedBy": "tester"})
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
        if p == "/v1/runs/run-1/cancel":
            return self._send(200, {"id": "run-1", "status": "cancelled"})
        if p == "/v1/runs/run-1/approvals":
            STATE.approvals_requested += 1
            if not body.get("sideEffect"):
                return self._send(200, {"status": "approved", "required": False})
            return self._send(201, {"id": "ap-1", "status": "pending"})
        return self._send(404, {"code": "not_found", "message": "no"})


# --- Lightweight stand-ins for CrewAI's AgentAction / AgentFinish, so the unit
# tests don't need crewai installed. The adapter duck-types on `.tool`, so these
# match the real objects' shape (an AgentAction has .tool/.tool_input; an
# AgentFinish does not). Class names match too, exercising the name-based fallback.
class AgentAction:
    def __init__(self, tool="", tool_input="", thought="", text=""):
        self.tool = tool
        self.tool_input = tool_input
        self.thought = thought
        self.text = text


class AgentFinish:
    def __init__(self, output="", thought="", text=""):
        self.output = output
        self.thought = thought
        self.text = text


class CrewAIAdapterTest(unittest.TestCase):
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

    def test_callback_ticks_one_step_per_call(self):
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=5)) as run:
            cb = RiskKernelStepCallback(run)
            cb(AgentFinish(output="done"))
            cb(AgentAction(tool="search", tool_input="q"))
            self.assertEqual(STATE.steps, 2)  # one governed step per agent step

    def test_callback_enforces_loop_budget(self):
        # The real proof at the unit level: the callback must raise BudgetExceeded
        # when the loop budget is spent (CrewAI calls step_callback synchronously and
        # the executor re-raises an unknown error, so this halt stops the crew).
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=2)) as run:
            cb = RiskKernelStepCallback(run)
            cb(AgentFinish(output="step 1"))            # step 1
            cb(AgentAction(tool="search", tool_input=""))  # step 2
            with self.assertRaises(BudgetExceeded) as ctx:
                cb(AgentFinish(output="step 3"))        # step 3 → over budget
            self.assertEqual(ctx.exception.reason, "loop_budget_exceeded")

    def test_callback_halt_is_not_swallowed(self):
        # Guards the propagation contract: __call__ must let BudgetExceeded escape so
        # CrewAI's loop (which re-raises unknown errors) actually halts the crew. If
        # the adapter ever caught it internally, this would fail.
        STATE.loop_budget = 0  # the stub halts from the very first step
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=0)) as run:
            cb = RiskKernelStepCallback(run)
            with self.assertRaises(BudgetExceeded):
                cb(AgentFinish(output="first step is already over budget"))

    def test_tool_gating_off_by_default(self):
        # Default: gate_tools is False, so a tool action never asks for approval.
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=5)) as run:
            cb = RiskKernelStepCallback(run)
            cb(AgentAction(tool="deploy", tool_input="ship it"))
            self.assertEqual(STATE.approvals_requested, 0)
            self.assertEqual(STATE.steps, 1)  # still ticks the step

    def test_tool_gating_on_requests_approval_then_ticks(self):
        # gate_tools=True: an AgentAction is gated (stub approves on 2nd poll), then
        # the step is counted.
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=5)) as run:
            cb = RiskKernelStepCallback(run, gate_tools=True, tool_side_effect="exec")
            cb(AgentAction(tool="deploy", tool_input="ship it"))
            self.assertEqual(STATE.approvals_requested, 1)
            self.assertEqual(STATE.steps, 1)

    def test_tool_gating_does_not_gate_final_answer(self):
        # An AgentFinish is not a tool call: gate_tools=True must not gate it, even
        # though it still ticks a governed step.
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=5)) as run:
            cb = RiskKernelStepCallback(run, gate_tools=True)
            cb(AgentFinish(output="final answer"))
            self.assertEqual(STATE.approvals_requested, 0)
            self.assertEqual(STATE.steps, 1)

    def test_tool_gating_denied_raises_before_step(self):
        # A denied tool must raise ApprovalDenied and NOT tick a step (the gate runs
        # before run.step()), so a blocked side effect doesn't burn budget.
        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=5)) as run:
            run._client.get_approval = lambda _id: {
                "id": "ap-1", "status": "denied", "reason": "no"}
            cb = RiskKernelStepCallback(run, gate_tools=True)
            with self.assertRaises(ApprovalDenied):
                cb(AgentAction(tool="deploy", tool_input="ship it"))
            self.assertEqual(STATE.steps, 0)  # gate denied before the step was counted

    def test_action_detection_by_attribute_and_classname(self):
        # The adapter duck-types: a `.tool` attribute marks a tool action; the class
        # name "AgentAction" is the fallback when `.tool` is empty.
        from riskkernel.adapters.crewai import _is_tool_action

        self.assertTrue(_is_tool_action(AgentAction(tool="x")))   # has a tool name
        self.assertTrue(_is_tool_action(AgentAction(tool="")))    # name-based fallback
        self.assertFalse(_is_tool_action(AgentFinish(output="y")))
        self.assertFalse(_is_tool_action(None))

    @unittest.skipUnless(_has_crewai(), "crewai not installed")
    def test_crewai_integration_halts_runaway_crew(self):
        # The real proof: a CrewAI agent that would loop is hard-stopped at its loop
        # budget. We drive a real CrewAI Agent with a stub LLM that always asks to use
        # a tool (so the agent never finishes on its own) and assert the governor's
        # BudgetExceeded propagates out of crew/agent execution.
        from crewai import Agent, Crew, Task
        from crewai.tools import tool as crewai_tool

        @crewai_tool("noop")
        def noop(query: str) -> str:
            """A do-nothing tool the agent keeps calling."""
            return "keep going"

        # A minimal stub LLM that always emits an Action (never a Final Answer), so
        # the agent would loop forever without the governor. We avoid any network/key
        # by subclassing CrewAI's BaseLLM and returning canned ReAct text.
        from crewai.llms.base_llm import BaseLLM

        class LoopingLLM(BaseLLM):
            def __init__(self):
                super().__init__(model="stub")

            def call(self, messages, tools=None, callbacks=None,
                     available_functions=None, **kwargs):
                return (
                    "Thought: I should use the tool again.\n"
                    "Action: noop\n"
                    'Action Input: {"query": "again"}'
                )

            def supports_function_calling(self) -> bool:
                return False

        with self.rt.governed_run(name="t", budget=self.rt.budget(loops=3)) as run:
            cb = RiskKernelStepCallback(run)
            agent = Agent(
                role="looper", goal="loop forever", backstory="a runaway agent",
                tools=[noop], llm=LoopingLLM(), step_callback=cb,
                max_iter=100, verbose=False,
            )
            task = Task(description="keep using the tool", expected_output="never",
                        agent=agent)
            crew = Crew(agents=[agent], tasks=[task], verbose=False)
            with self.assertRaises(BudgetExceeded):
                crew.kickoff()
            # The governor capped the loop: no more steps than the budget were taken.
            self.assertGreaterEqual(STATE.steps, 3)


if __name__ == "__main__":
    unittest.main()
