"""PydanticAI adapter tests — governance behavior against a fake Run, no daemon, no
third-party deps. The wrapper imports even without pydantic-ai installed (lazy base
class), so these exercise the enforcement path on stdlib alone; a real PydanticAI
integration test is gated behind skipUnless.
"""

import asyncio
import unittest

from riskkernel.adapters.pydantic_ai import GovernedModel, govern, _tool_calls
from riskkernel.errors import ApprovalDenied, BudgetExceeded


def _has_pydantic_ai() -> bool:
    try:
        import pydantic_ai  # noqa: F401

        return True
    except Exception:
        return False


class FakeRun:
    """A stand-in for runtime.Run: counts steps and halts past a loop budget,
    mirroring how the daemon's BeginStep raises BudgetExceeded when the budget is
    spent. No HTTP, no daemon."""

    def __init__(self, loop_budget=None):
        self.steps = 0
        self.loop_budget = loop_budget
        self.id = "run-1"

    def step(self):
        self.steps += 1
        if self.loop_budget is not None and self.steps > self.loop_budget:
            raise BudgetExceeded("loop_budget_exceeded")
        return self.steps


def _model_base():
    # When pydantic-ai is installed, GovernedModel inherits WrapperModel, whose
    # __init__ runs infer_model(wrapped) — which only accepts a real Model instance
    # (or a model-name string). So the fake model must BE a real Model for these unit
    # tests to construct GovernedModel. When pydantic-ai is absent, the adapter falls
    # back to object and stores the wrapped model directly, so a plain object works.
    try:
        from pydantic_ai.models import Model  # type: ignore

        return Model
    except Exception:
        return object


class _FakeModel(_model_base()):  # type: ignore[misc]
    """A stand-in for a PydanticAI Model: records that it was asked, and returns a
    canned response. Lets us exercise GovernedModel.request without (and with)
    pydantic-ai installed."""

    def __init__(self, response):
        self.response = response
        self.requests = 0

    async def request(self, messages, model_settings, model_request_parameters):
        self.requests += 1
        return self.response

    # Model is an ABC with abstract model_name/system properties; provide them so the
    # subclass is instantiable when pydantic-ai is present.
    @property
    def model_name(self) -> str:
        return "fake"

    @property
    def system(self) -> str:
        return "fake"


class _Resp:
    """A minimal ModelResponse stand-in: has `.parts` like the real one."""

    def __init__(self, parts):
        self.parts = parts


class _ToolCallPart:
    """A minimal ToolCallPart stand-in: duck-typed on `.tool_name` / `.args`, which
    is exactly what the adapter inspects to find proposed tool calls."""

    def __init__(self, tool_name, args=None):
        self.tool_name = tool_name
        self.args = args


class _TextPart:
    """A non-tool response part (final text): has no `.tool_name`, so it is never
    gated."""

    def __init__(self, content):
        self.content = content


class _FakeGate:
    """Records gate calls; can be set to deny like a human denying a tool."""

    def __init__(self, deny=False):
        self.deny = deny
        self.calls = []

    def require(self, tool, side_effect="", arguments=None, timeout=None):
        self.calls.append({"tool": tool, "side_effect": side_effect,
                           "arguments": arguments, "timeout": timeout})
        if self.deny:
            raise ApprovalDenied(tool, "no")


def _run(coro):
    return asyncio.run(coro)


class PydanticAIAdapterTest(unittest.TestCase):
    def test_module_imports_without_pydantic_ai(self):
        # The lazy base-class fallback must let the SDK import the adapter even with
        # no pydantic-ai installed (it is not a dependency).
        self.assertTrue(callable(GovernedModel))
        self.assertTrue(callable(govern))

    def test_request_ticks_one_step_per_model_request(self):
        run = FakeRun()
        inner = _FakeModel(_Resp([_TextPart("done")]))
        gm = govern(inner, run)
        _run(gm.request([], None, None))
        self.assertEqual(run.steps, 1)        # one governed step per model request
        self.assertEqual(inner.requests, 1)   # and it delegated to the real model

    def test_request_enforces_loop_budget(self):
        run = FakeRun(loop_budget=2)
        inner = _FakeModel(_Resp([_TextPart("ok")]))
        gm = govern(inner, run)
        _run(gm.request([], None, None))      # step 1
        _run(gm.request([], None, None))      # step 2
        with self.assertRaises(BudgetExceeded) as cm:
            _run(gm.request([], None, None))  # step 3 -> over budget
        self.assertEqual(cm.exception.reason, "loop_budget_exceeded")

    def test_budget_halt_is_not_swallowed_and_skips_the_request(self):
        # The propagation contract: the wrapper must let BudgetExceeded escape (so
        # PydanticAI, which only retries its own ModelRetry, halts the agent), and it
        # must tick the step BEFORE delegating, so an over-budget run never spends on
        # the underlying model request.
        run = FakeRun(loop_budget=0)          # over budget from the very first step
        inner = _FakeModel(_Resp([_TextPart("ok")]))
        gm = govern(inner, run)
        with self.assertRaises(BudgetExceeded):
            _run(gm.request([], None, None))
        self.assertEqual(inner.requests, 0)   # the model was never called

    def test_tool_gating_off_by_default(self):
        # Default: gate_tools is False, so proposed tool calls are never gated.
        run = FakeRun()
        inner = _FakeModel(_Resp([_ToolCallPart("deploy", {"x": 1})]))
        gm = govern(inner, run)
        gate = _FakeGate()
        gm._gate = gate
        _run(gm.request([], None, None))
        self.assertEqual(gate.calls, [])      # gate_tools defaults off
        self.assertEqual(run.steps, 1)        # the step still ticks

    def test_tool_gating_on_passes_tool_name_and_side_effect(self):
        run = FakeRun()
        inner = _FakeModel(_Resp([_ToolCallPart("deploy", {"env": "prod"})]))
        gm = govern(inner, run, gate_tools=True, tool_side_effect="exec", timeout=5)
        gate = _FakeGate()
        gm._gate = gate
        _run(gm.request([], None, None))
        self.assertEqual(len(gate.calls), 1)
        self.assertEqual(gate.calls[0]["tool"], "deploy")
        self.assertEqual(gate.calls[0]["side_effect"], "exec")
        self.assertEqual(gate.calls[0]["timeout"], 5)
        self.assertEqual(run.steps, 1)

    def test_tool_gating_gates_every_proposed_tool_call(self):
        # A single model response may propose several tool calls; each is gated.
        run = FakeRun()
        inner = _FakeModel(_Resp([
            _ToolCallPart("search", {"q": "a"}),
            _ToolCallPart("write", {"path": "/x"}),
        ]))
        gm = govern(inner, run, gate_tools=True)
        gate = _FakeGate()
        gm._gate = gate
        _run(gm.request([], None, None))
        self.assertEqual([c["tool"] for c in gate.calls], ["search", "write"])

    def test_tool_gating_does_not_gate_a_final_text_answer(self):
        # A text-only response proposes no tool calls: gate_tools=True must not gate
        # it, even though the request still ticks a governed step.
        run = FakeRun()
        inner = _FakeModel(_Resp([_TextPart("final answer")]))
        gm = govern(inner, run, gate_tools=True)
        gate = _FakeGate()
        gm._gate = gate
        _run(gm.request([], None, None))
        self.assertEqual(gate.calls, [])
        self.assertEqual(run.steps, 1)

    def test_tool_gating_denied_raises_and_halts(self):
        # A denied tool must raise ApprovalDenied, which propagates out of the run and
        # halts the agent before the tool executes.
        run = FakeRun()
        inner = _FakeModel(_Resp([_ToolCallPart("deploy", {})]))
        gm = govern(inner, run, gate_tools=True)
        gm._gate = _FakeGate(deny=True)
        with self.assertRaises(ApprovalDenied):
            _run(gm.request([], None, None))

    def test_tool_calls_helper_is_duck_typed(self):
        # The adapter duck-types: a part with `.tool_name` is a tool call; a text part
        # (no `.tool_name`) is not. Works without importing pydantic-ai.
        parts = [_ToolCallPart("x"), _TextPart("y"), _ToolCallPart("z")]
        found = [p.tool_name for p in _tool_calls(_Resp(parts))]
        self.assertEqual(found, ["x", "z"])
        self.assertEqual(list(_tool_calls(_Resp([]))), [])
        self.assertEqual(list(_tool_calls(_Resp(None))), [])

    @unittest.skipUnless(_has_pydantic_ai(), "pydantic-ai not installed")
    def test_pydantic_ai_integration_halts_runaway_agent(self):
        # The real proof: a PydanticAI agent that would loop forever is hard-stopped
        # at its loop budget. We drive a real Agent with a FunctionModel that always
        # proposes a tool call (so the agent never produces a final answer) and assert
        # the governor's BudgetExceeded propagates out of agent.run_sync — PydanticAI
        # only retries its own ModelRetry, so a plain exception halts the agent.
        from pydantic_ai import Agent
        from pydantic_ai.messages import ModelResponse, ToolCallPart
        from pydantic_ai.models.function import FunctionModel

        def always_call_tool(messages, info):
            # Never emit a text part -> the run never reaches a final output -> the
            # agent loops on tool calls forever without the governor.
            return ModelResponse(parts=[ToolCallPart(tool_name="spin", args={})])

        run = FakeRun(loop_budget=3)
        agent = Agent(govern(FunctionModel(always_call_tool), run))

        @agent.tool_plain
        def spin() -> str:
            return "still going"

        with self.assertRaises(BudgetExceeded) as cm:
            agent.run_sync("go")
        self.assertEqual(cm.exception.reason, "loop_budget_exceeded")
        # The governor capped the loop at budget + 1 (the request that tripped it).
        self.assertEqual(run.steps, 4)

    @unittest.skipUnless(_has_pydantic_ai(), "pydantic-ai not installed")
    def test_pydantic_ai_integration_tool_gating_denied_halts_agent(self):
        # gate_tools=True with a denial: a real agent's proposed tool call is routed
        # through the gate, the denial raises ApprovalDenied, and it propagates out of
        # agent.run_sync — the tool never executes.
        from pydantic_ai import Agent
        from pydantic_ai.messages import ModelResponse, ToolCallPart
        from pydantic_ai.models.function import FunctionModel

        executed = {"deploy": False}

        def call_deploy(messages, info):
            return ModelResponse(parts=[ToolCallPart(tool_name="deploy", args={})])

        run = FakeRun(loop_budget=10)
        gm = govern(FunctionModel(call_deploy), run, gate_tools=True)
        gm._gate = _FakeGate(deny=True)
        agent = Agent(gm)

        @agent.tool_plain
        def deploy() -> str:
            executed["deploy"] = True
            return "deployed"

        with self.assertRaises(ApprovalDenied):
            agent.run_sync("go")
        self.assertFalse(executed["deploy"])  # the denied tool never ran


if __name__ == "__main__":
    unittest.main()
