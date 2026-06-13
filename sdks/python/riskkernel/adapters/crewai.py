"""CrewAI adapter: a ``step_callback`` that enforces a governed run's loop and time
budgets, ticking one step per agent step. Wire it onto your ``Agent`` (or the whole
``Crew``) and point your CrewAI LLM at the governing proxy (``run.proxy_config()``)
for token/cost/budget metering; this callback adds the outer-loop enforcement the
proxy can't see.

    from crewai import Agent, Crew
    from riskkernel.adapters.crewai import RiskKernelStepCallback

    cb = RiskKernelStepCallback(run)
    agent = Agent(role=..., goal=..., backstory=..., step_callback=cb)
    # or apply it to every agent in the crew at once:
    crew = Crew(agents=[...], tasks=[...], step_callback=cb)

A BudgetExceeded raised here propagates out of the crew and halts it. CrewAI calls
``step_callback`` synchronously inside the agent's tool-use loop, once per step
(after each action or final answer), and the executor re-raises an unknown error out
of the loop rather than swallowing it — so the deterministic halt surfaces to the
caller and stops the run. This is unlike CrewAI's *event bus*
(``crewai.events``): bus handlers run fire-and-forget in a thread pool / via
``asyncio.gather(return_exceptions=True)`` and the agent loop never awaits the
returned future, so an exception raised in a bus listener is captured and dropped —
it would NOT halt the crew. The ``step_callback`` is the mechanism that does.

Supported API: CrewAI's ``step_callback`` on ``Agent``/``Crew`` (stable through the
1.x line; pinned/tested against ``crewai`` >= 0.80, < 2). The callback receives a
``crewai.agents.parser.AgentAction`` (a tool call: has ``.tool`` / ``.tool_input``)
or an ``AgentFinish`` (the final answer). We tick one governed step per call — one
agent step == one governed step — and, when ``gate_tools`` is set, route each
``AgentAction``'s tool through the approval gate before its step is counted. The
answer object is duck-typed, so this module imports cleanly without CrewAI present
(it is NOT a dependency of the SDK).
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..runtime import Run


class RiskKernelStepCallback:
    """Enforces loop/time budgets and (optionally) gates tools on approval.

    Use it as a CrewAI ``step_callback`` — it is callable, so pass the instance
    directly to ``Agent(step_callback=...)`` or ``Crew(step_callback=...)``.

    Args:
        run: the governed Run.
        gate_tools: if True, every tool call (an ``AgentAction``) must pass the
            approval gate before its step is counted; a denial raises and halts.
        tool_side_effect: side-effect label reported for gated tools.
        timeout: max seconds to await a human decision on a gated tool.
    """

    def __init__(self, run: Run, gate_tools: bool = False,
                 tool_side_effect: str = "tool", timeout: Optional[float] = None):
        self.run = run
        self.gate_tools = gate_tools
        self.tool_side_effect = tool_side_effect
        self.timeout = timeout
        self._gate = ApprovalGate(run)

    # CrewAI invokes the step callback with the step's answer (AgentAction for a
    # tool call, AgentFinish for the final answer). One call == one governed step.
    def __call__(self, step_output: Any = None, *args: Any, **kwargs: Any) -> None:
        if self.gate_tools and _is_tool_action(step_output):
            name = _tool_name(step_output)
            self._gate.require(name or "tool", side_effect=self.tool_side_effect,
                               arguments={"input": _stringify(_tool_input(step_output))},
                               timeout=self.timeout)
        # Raises BudgetExceeded when the loop/time budget is spent; the crew halts.
        self.run.step()


# Backwards-friendly alias matching the other adapters' "Handler" naming, so the
# import reads the same shape regardless of which framework you came from.
RiskKernelStepCallbackHandler = RiskKernelStepCallback


def _is_tool_action(step_output: Any) -> bool:
    """True if this step is a tool call (a CrewAI ``AgentAction``). Duck-typed: an
    AgentAction has a ``.tool`` attribute; an AgentFinish does not. Falls back to the
    class name so it works without importing CrewAI."""
    if step_output is None:
        return False
    if getattr(step_output, "tool", None):
        return True
    return type(step_output).__name__ == "AgentAction"


def _tool_name(step_output: Any) -> str:
    """The tool name from an AgentAction (``.tool``)."""
    name = getattr(step_output, "tool", None)
    return str(name) if name else ""


def _tool_input(step_output: Any) -> Any:
    """The tool input from an AgentAction (``.tool_input``)."""
    return getattr(step_output, "tool_input", None)


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
