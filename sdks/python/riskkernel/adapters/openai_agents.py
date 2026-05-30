"""OpenAI Agents SDK adapter: lifecycle hooks that tick a governed step per agent
turn and gate tools through the approval gate.

    from riskkernel.adapters.openai_agents import RiskKernelRunHooks
    hooks = RiskKernelRunHooks(run, gate_tools=True)
    await Runner.run(agent, input, hooks=hooks)

The OpenAI Agents SDK calls ``on_agent_start``/``on_tool_start`` (async). We tick a
step on each agent start (loop/time enforcement) and, when ``gate_tools`` is set,
await approval before a tool runs — raising ApprovalDenied to block it.
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..runtime import Run


def _base_hooks():
    try:
        from agents import RunHooks  # type: ignore  (openai-agents)
        return RunHooks
    except Exception:
        return object


class RiskKernelRunHooks(_base_hooks()):  # type: ignore[misc]
    """RunHooks that bind a governed run to an OpenAI Agents run."""

    def __init__(self, run: Run, gate_tools: bool = False,
                 tool_side_effect: str = "tool", timeout: Optional[float] = None):
        self.run = run
        self.gate_tools = gate_tools
        self.tool_side_effect = tool_side_effect
        self.timeout = timeout
        self._gate = ApprovalGate(run)

    async def on_agent_start(self, context: Any = None, agent: Any = None, **kwargs: Any) -> None:
        # One agent turn == one governed step (enforces loop/time budgets).
        self.run.step()

    async def on_tool_start(self, context: Any = None, agent: Any = None,
                            tool: Any = None, **kwargs: Any) -> None:
        if not self.gate_tools:
            return
        name = getattr(tool, "name", None) or str(tool)
        self._gate.require(name, side_effect=self.tool_side_effect, timeout=self.timeout)
