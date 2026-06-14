"""LlamaIndex adapter tests — governance behavior against a fake Run, no daemon,
no third-party deps. The handler imports even without llama-index installed (lazy
base class), so these exercise the enforcement path on stdlib alone; a real
LlamaIndex integration test is gated behind skipUnless.
"""

import unittest

from riskkernel.adapters.llama_index import RiskKernelCallbackHandler
from riskkernel.errors import ApprovalDenied, BudgetExceeded


def _has_llama_index() -> bool:
    try:
        import llama_index.core  # noqa: F401

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


class LlamaIndexAdapterTest(unittest.TestCase):
    def test_module_imports_without_llama_index(self):
        # The lazy base-class fallback must let the SDK import the adapter even with
        # no llama-index installed (it is not a dependency).
        self.assertTrue(callable(RiskKernelCallbackHandler))

    def test_llm_event_ticks_one_step(self):
        run = FakeRun()
        h = RiskKernelCallbackHandler(run)
        # on_event_start must return the event_id so the callback manager can
        # correlate the matching on_event_end.
        self.assertEqual(h.on_event_start("llm", event_id="e1"), "e1")
        self.assertEqual(run.steps, 1)

    def test_llm_event_enforces_loop_budget(self):
        run = FakeRun(loop_budget=2)
        h = RiskKernelCallbackHandler(run)
        h.on_event_start("llm", event_id="e1")          # step 1
        h.on_event_start("llm", event_id="e2")          # step 2
        with self.assertRaises(BudgetExceeded) as cm:
            h.on_event_start("llm", event_id="e3")      # step 3 -> over budget
        self.assertEqual(cm.exception.reason, "loop_budget_exceeded")
        # The halt is surfaced, not swallowed; the step counter stopped at the cap+1.
        self.assertEqual(run.steps, 3)

    def test_non_llm_events_do_not_tick_a_step(self):
        run = FakeRun()
        h = RiskKernelCallbackHandler(run)
        for et in ("retrieve", "embedding", "query", "synthesize"):
            h.on_event_start(et, event_id="e")
        self.assertEqual(run.steps, 0)

    def test_function_call_not_gated_by_default(self):
        run = FakeRun()
        h = RiskKernelCallbackHandler(run)
        gate = _FakeGate()
        h._gate = gate
        h.on_event_start("function_call", event_id="e",
                         payload={"tool": _ToolMeta("deploy")})
        self.assertEqual(gate.calls, [])                # gate_tools defaults off
        self.assertEqual(run.steps, 0)                  # and it isn't an LLM step

    def test_function_call_gated_passes_tool_name(self):
        run = FakeRun()
        h = RiskKernelCallbackHandler(run, gate_tools=True, tool_side_effect="exec")
        gate = _FakeGate()
        h._gate = gate
        h.on_event_start("function_call", event_id="e",
                         payload={"tool": _ToolMeta("deploy")})
        self.assertEqual(len(gate.calls), 1)
        self.assertEqual(gate.calls[0]["tool"], "deploy")
        self.assertEqual(gate.calls[0]["side_effect"], "exec")

    def test_function_call_gated_denied_raises(self):
        run = FakeRun()
        h = RiskKernelCallbackHandler(run, gate_tools=True)
        h._gate = _FakeGate(deny=True)
        with self.assertRaises(ApprovalDenied):
            h.on_event_start("function_call", event_id="e",
                             payload={"function_call": {"name": "deploy"}})

    def test_enum_like_event_type_is_normalized(self):
        # CBEventType has a .value; the handler matches on the string value, so an
        # enum-like object with .value == "llm" must still tick a step.
        run = FakeRun()
        h = RiskKernelCallbackHandler(run)

        class _EventType:
            value = "LLM"

        h.on_event_start(_EventType(), event_id="e")
        self.assertEqual(run.steps, 1)

    @unittest.skipUnless(_has_llama_index(), "llama-index-core not installed")
    def test_llama_index_integration_stops_runaway_loop(self):
        # The real proof: a runaway loop of LlamaIndex LLM calls must actually stop.
        # LlamaIndex's CallbackManager doesn't swallow handler exceptions, so the
        # BudgetExceeded raised on the 3rd LLM call propagates out of the call.
        from llama_index.core.callbacks import CallbackManager
        from llama_index.core.llms import MockLLM

        run = FakeRun(loop_budget=2)
        cm = CallbackManager([RiskKernelCallbackHandler(run)])
        llm = MockLLM(callback_manager=cm)
        calls = 0
        with self.assertRaises(BudgetExceeded):
            while True:                                  # a deliberately runaway loop
                llm.complete("step")
                calls += 1
        self.assertEqual(calls, 2)                       # 2 allowed; the 3rd halted


class _ToolMeta:
    """A minimal ToolMetadata stand-in: has a .name like LlamaIndex's real one."""

    def __init__(self, name):
        self.name = name


if __name__ == "__main__":
    unittest.main()
