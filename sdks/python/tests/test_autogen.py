"""AutoGen adapter tests — governance behavior against a fake Run (no daemon, no
third-party deps), plus a real AutoGen integration test gated behind skipUnless.

The wrapper imports even without autogen installed (it is duck-typed and imports
nothing from autogen), so these exercise the enforcement path on stdlib alone. The
async create()/create_stream() methods are driven with asyncio.run.
"""

import asyncio
import unittest

from riskkernel.adapters.autogen import (
    GovernedChatCompletionClient,
    governed_run_errors,
    _typed_from_runtime_error,
)
from riskkernel.errors import ApprovalDenied, BudgetExceeded


def _has_autogen() -> bool:
    try:
        import autogen_agentchat  # noqa: F401
        import autogen_core  # noqa: F401

        return True
    except Exception:
        return False


def _run(coro):
    return asyncio.run(coro)


async def _drain_stream(client, *args, **kwargs):
    """Consume create_stream to completion, returning the list of chunks."""
    out = []
    async for chunk in client.create_stream(*args, **kwargs):
        out.append(chunk)
    return out


class FakeRun:
    """A stand-in for runtime.Run: counts steps and halts past a loop budget,
    mirroring how the daemon's BeginStep raises BudgetExceeded when the budget is
    spent. No HTTP, no daemon."""

    def __init__(self, loop_budget=None):
        self.steps = 0
        self.loop_budget = loop_budget

    def step(self):
        self.steps += 1
        if self.loop_budget is not None and self.steps > self.loop_budget:
            raise BudgetExceeded("loop_budget_exceeded")
        return self.steps


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


# ── Minimal stand-ins for AutoGen's model client and result objects, so the unit
# tests don't need autogen installed. The wrapper duck-types on these shapes:
# a CreateResult has .content (str OR list[FunctionCall]) and .finish_reason; a
# FunctionCall has .name / .arguments.
class _FunctionCall:
    def __init__(self, name, arguments=""):
        self.name = name
        self.arguments = arguments


class _CreateResult:
    def __init__(self, content, finish_reason="stop"):
        self.content = content
        self.finish_reason = finish_reason


class _FakeModelClient:
    """A model client whose create() returns a canned CreateResult and records the
    args it was called with. create_stream() yields a couple of string chunks then
    the final CreateResult, like a real AutoGen streaming client."""

    def __init__(self, result=None):
        self.result = result or _CreateResult("hello")
        self.create_calls = 0
        self.closed = False

    async def create(self, *args, **kwargs):
        self.create_calls += 1
        return self.result

    async def create_stream(self, *args, **kwargs):
        self.create_calls += 1
        yield "thinking"
        yield "more"
        yield self.result

    def actual_usage(self):
        return "actual"

    def total_usage(self):
        return "total"

    def count_tokens(self, *a, **k):
        return 7

    def remaining_tokens(self, *a, **k):
        return 100

    async def close(self):
        self.closed = True

    @property
    def model_info(self):
        return {"family": "fake"}

    @property
    def capabilities(self):
        return {"vision": False}

    # An attribute the wrapper doesn't name explicitly, to exercise __getattr__
    # delegation to the wrapped client.
    def component_config(self):
        return {"provider": "fake"}


class AutoGenAdapterTest(unittest.TestCase):
    def test_module_imports_without_autogen(self):
        # The wrapper is duck-typed and imports nothing from autogen, so the SDK can
        # import the adapter with no autogen installed (it is not a dependency).
        self.assertTrue(callable(GovernedChatCompletionClient))

    def test_create_ticks_one_step(self):
        run = FakeRun()
        client = GovernedChatCompletionClient(_FakeModelClient(), run)
        result = _run(client.create([]))
        self.assertEqual(run.steps, 1)              # one governed step per create()
        self.assertEqual(result.content, "hello")   # the real result is returned

    def test_create_enforces_loop_budget(self):
        # The real proof at the unit level: create() must raise BudgetExceeded when
        # the loop budget is spent. AutoGen does not catch model-client exceptions in
        # a single agent, so this halt propagates out of the agent.
        run = FakeRun(loop_budget=2)
        client = GovernedChatCompletionClient(_FakeModelClient(), run)
        _run(client.create([]))                     # step 1
        _run(client.create([]))                     # step 2
        with self.assertRaises(BudgetExceeded) as cm:
            _run(client.create([]))                 # step 3 -> over budget
        self.assertEqual(cm.exception.reason, "loop_budget_exceeded")

    def test_halt_is_not_swallowed(self):
        # Guards the propagation contract: create() must let BudgetExceeded escape so
        # AutoGen's agent (which does not wrap model-client errors) actually halts. The
        # step is ticked BEFORE delegating, so an over-budget call never reaches the
        # underlying client.
        run = FakeRun(loop_budget=0)
        inner = _FakeModelClient()
        client = GovernedChatCompletionClient(inner, run)
        with self.assertRaises(BudgetExceeded):
            _run(client.create([]))
        self.assertEqual(inner.create_calls, 0)     # halted before the real call

    def test_create_stream_ticks_one_step_and_yields_chunks(self):
        run = FakeRun()
        client = GovernedChatCompletionClient(_FakeModelClient(), run)
        chunks = _run(_drain_stream(client, []))
        self.assertEqual(run.steps, 1)              # one governed step per stream
        self.assertEqual(chunks[:2], ["thinking", "more"])
        self.assertEqual(chunks[-1].content, "hello")  # final chunk is the result

    def test_create_stream_enforces_loop_budget(self):
        run = FakeRun(loop_budget=1)
        client = GovernedChatCompletionClient(_FakeModelClient(), run)
        _run(_drain_stream(client, []))             # step 1
        with self.assertRaises(BudgetExceeded):
            _run(_drain_stream(client, []))         # step 2 -> over budget

    def test_tool_gating_off_by_default(self):
        # Default: gate_tools is False, so a tool-call result never asks for approval.
        run = FakeRun()
        result = _CreateResult([_FunctionCall("deploy", '{"x":1}')])
        client = GovernedChatCompletionClient(_FakeModelClient(result), run)
        gate = _FakeGate()
        client._gate = gate
        _run(client.create([]))
        self.assertEqual(gate.calls, [])            # gate_tools defaults off
        self.assertEqual(run.steps, 1)              # still ticks the step

    def test_tool_gating_on_gates_each_function_call(self):
        # gate_tools=True: every FunctionCall in the result is gated, with its name
        # and side-effect label passed through.
        run = FakeRun()
        result = _CreateResult([_FunctionCall("deploy", '{"x":1}'),
                                _FunctionCall("notify", "{}")])
        client = GovernedChatCompletionClient(
            _FakeModelClient(result), run, gate_tools=True, tool_side_effect="exec")
        gate = _FakeGate()
        client._gate = gate
        _run(client.create([]))
        self.assertEqual([c["tool"] for c in gate.calls], ["deploy", "notify"])
        self.assertEqual(gate.calls[0]["side_effect"], "exec")
        self.assertEqual(run.steps, 1)

    def test_tool_gating_does_not_gate_text_result(self):
        # A plain-text result (content is a str, not a list of FunctionCalls) is not a
        # tool call: gate_tools=True must not gate it, though it still ticks a step.
        run = FakeRun()
        client = GovernedChatCompletionClient(
            _FakeModelClient(_CreateResult("final answer")), run, gate_tools=True)
        gate = _FakeGate()
        client._gate = gate
        _run(client.create([]))
        self.assertEqual(gate.calls, [])
        self.assertEqual(run.steps, 1)

    def test_tool_gating_denied_raises_after_step(self):
        # A denied tool must raise ApprovalDenied. The step is ticked first (the model
        # request happened); the gate then blocks the side effect from running.
        run = FakeRun()
        result = _CreateResult([_FunctionCall("deploy")])
        client = GovernedChatCompletionClient(
            _FakeModelClient(result), run, gate_tools=True)
        client._gate = _FakeGate(deny=True)
        with self.assertRaises(ApprovalDenied):
            _run(client.create([]))

    def test_tool_gating_in_stream(self):
        # The final CreateResult chunk of a stream is gated the same as a create().
        run = FakeRun()
        result = _CreateResult([_FunctionCall("deploy")])
        client = GovernedChatCompletionClient(
            _FakeModelClient(result), run, gate_tools=True)
        gate = _FakeGate()
        client._gate = gate
        _run(_drain_stream(client, []))
        self.assertEqual([c["tool"] for c in gate.calls], ["deploy"])

    def test_protocol_methods_delegate_to_wrapped_client(self):
        # The wrapper must be a drop-in: every other ChatCompletionClient method/attr
        # delegates to the wrapped client unchanged.
        run = FakeRun()
        inner = _FakeModelClient()
        client = GovernedChatCompletionClient(inner, run)
        self.assertEqual(client.actual_usage(), "actual")
        self.assertEqual(client.total_usage(), "total")
        self.assertEqual(client.count_tokens([]), 7)
        self.assertEqual(client.remaining_tokens([]), 100)
        self.assertEqual(client.model_info, {"family": "fake"})
        self.assertEqual(client.capabilities, {"vision": False})
        # An un-named method falls through __getattr__ to the wrapped client.
        self.assertEqual(client.component_config(), {"provider": "fake"})
        _run(client.close())
        self.assertTrue(inner.closed)

    # ── The team-propagation asymmetry: a team re-raises a budget halt as a plain
    # RuntimeError("BudgetExceeded: ..."); governed_run_errors() restores the type.
    def test_typed_from_runtime_error_budget(self):
        e = RuntimeError("BudgetExceeded: run halted: loop_budget_exceeded")
        typed = _typed_from_runtime_error(e)
        self.assertIsInstance(typed, BudgetExceeded)
        self.assertEqual(typed.reason, "loop_budget_exceeded")

    def test_typed_from_runtime_error_budget_with_traceback(self):
        # SerializableException may append a traceback on following lines; only the
        # first line is matched.
        e = RuntimeError(
            "BudgetExceeded: run halted: token_budget_exceeded\nTraceback:\n  ...")
        typed = _typed_from_runtime_error(e)
        self.assertIsInstance(typed, BudgetExceeded)
        self.assertEqual(typed.reason, "token_budget_exceeded")

    def test_typed_from_runtime_error_approval(self):
        e = RuntimeError("ApprovalDenied: approval denied for deploy: nope")
        typed = _typed_from_runtime_error(e)
        self.assertIsInstance(typed, ApprovalDenied)
        self.assertEqual(typed.tool, "deploy")
        self.assertEqual(typed.reason, "nope")

    def test_typed_from_runtime_error_ignores_unrelated(self):
        # A RuntimeError that isn't one of ours is left alone (returns None).
        self.assertIsNone(_typed_from_runtime_error(RuntimeError("some other error")))

    def test_governed_run_errors_reraises_typed_budget(self):
        # The context manager converts a team's wrapped RuntimeError back to the typed
        # BudgetExceeded so callers can `except rk.BudgetExceeded`.
        with self.assertRaises(BudgetExceeded) as cm:
            with governed_run_errors():
                raise RuntimeError("BudgetExceeded: run halted: loop_budget_exceeded")
        self.assertEqual(cm.exception.reason, "loop_budget_exceeded")

    def test_governed_run_errors_passes_through_other_errors(self):
        # An unrelated RuntimeError is not touched.
        with self.assertRaises(RuntimeError) as cm:
            with governed_run_errors():
                raise RuntimeError("kaboom")
        self.assertEqual(str(cm.exception), "kaboom")

    def test_governed_run_errors_no_error(self):
        # No exception -> the block runs and exits cleanly.
        with governed_run_errors():
            x = 1 + 1
        self.assertEqual(x, 2)

    @unittest.skipUnless(_has_autogen(), "autogen-agentchat / autogen-core not installed")
    def test_autogen_integration_halts_runaway_agent(self):
        # The real proof: a real AutoGen AssistantAgent driven by a model client that
        # always asks to call a tool (so the agent loops) is hard-stopped at its loop
        # budget by the governor. We wrap a real ChatCompletionClient implementation
        # with GovernedChatCompletionClient and assert the BudgetExceeded surfaces.
        from autogen_agentchat.agents import AssistantAgent
        from autogen_core import CancellationToken
        from autogen_core.models import (
            ChatCompletionClient,
            CreateResult,
            FunctionExecutionResultMessage,
            ModelInfo,
            RequestUsage,
        )
        from autogen_core import FunctionCall
        from autogen_core.tools import FunctionTool

        def noop(query: str) -> str:
            """A do-nothing tool the agent keeps calling."""
            return "keep going"

        tool = FunctionTool(noop, description="a no-op tool")

        # A real ChatCompletionClient that always returns a tool call, so the agent
        # never finishes on its own and would loop forever without the governor. No
        # network, no key. We implement only what AssistantAgent needs.
        class LoopingClient(ChatCompletionClient):
            def __init__(self):
                self._total = RequestUsage(prompt_tokens=0, completion_tokens=0)

            async def create(self, messages, *, tools=[], tool_choice="auto",
                             json_output=None, extra_create_args={},
                             cancellation_token=None):
                # If the last message is a tool result, ask for the tool again — a loop.
                return CreateResult(
                    finish_reason="function_calls",
                    content=[FunctionCall(id="1", name="noop",
                                          arguments='{"query": "again"}')],
                    usage=RequestUsage(prompt_tokens=1, completion_tokens=1),
                    cached=False,
                )

            async def create_stream(self, *args, **kwargs):  # pragma: no cover
                raise NotImplementedError

            async def close(self):
                return None

            def actual_usage(self):
                return self._total

            def total_usage(self):
                return self._total

            def count_tokens(self, messages, *, tools=[]):
                return 0

            def remaining_tokens(self, messages, *, tools=[]):
                return 1000

            @property
            def capabilities(self):
                return self.model_info

            @property
            def model_info(self):
                return ModelInfo(vision=False, function_calling=True,
                                 json_output=False, family="unknown",
                                 structured_output=False)

        run = FakeRun(loop_budget=3)
        client = GovernedChatCompletionClient(LoopingClient(), run)
        agent = AssistantAgent(
            "looper", model_client=client, tools=[tool],
            reflect_on_tool_use=False, max_tool_iterations=100,
        )

        async def go():
            await agent.run(task="loop forever", cancellation_token=CancellationToken())

        # A single agent run propagates the model-client exception unwrapped, so the
        # typed BudgetExceeded reaches us directly (no governed_run_errors needed here).
        with self.assertRaises(BudgetExceeded):
            _run(go())
        # The governor capped the loop: it stopped at the budget + the over-budget tick.
        self.assertGreaterEqual(run.steps, 3)


if __name__ == "__main__":
    unittest.main()
