"""LangChain / LangGraph adapter: a callback handler that enforces a governed
run's loop and time budgets, ticking one step per LLM call. Point your LangChain
LLM at the governing proxy (``run.proxy_config()``) for token/cost/budget metering;
this handler adds the outer-loop enforcement the proxy can't see.

    from riskkernel.adapters.langchain import RiskKernelCallbackHandler
    handler = RiskKernelCallbackHandler(run)
    llm.invoke(prompt, config={"callbacks": [handler]})

A BudgetExceeded raised here propagates out of the LangChain call, halting the
chain — exactly when the run is out of budget.
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..runtime import Run


def _base_handler():
    # Inherit the real base class when LangChain is installed (best integration);
    # otherwise fall back to object so the module still imports.
    try:
        from langchain_core.callbacks import BaseCallbackHandler  # type: ignore
        return BaseCallbackHandler
    except Exception:
        try:
            from langchain.callbacks.base import BaseCallbackHandler  # type: ignore
            return BaseCallbackHandler
        except Exception:
            return object


class RiskKernelCallbackHandler(_base_handler()):  # type: ignore[misc]
    """Enforces loop/time budgets and (optionally) gates tools on approval.

    Args:
        run: the governed Run.
        gate_tools: if True, every tool call must pass the approval gate.
        tool_side_effect: side-effect label reported for gated tools.
    """

    # LangChain swallows exceptions raised inside a callback — it logs them and
    # keeps running — UNLESS the handler sets raise_error=True. Without this, a
    # BudgetExceeded (or ApprovalDenied) raised in a hook below would be silently
    # dropped and the chain would keep spending past its budget. This single flag
    # is what makes the deterministic halt actually stop the LangChain run.
    raise_error = True

    def __init__(self, run: Run, gate_tools: bool = False,
                 tool_side_effect: str = "tool"):
        self.run = run
        self.gate_tools = gate_tools
        self.tool_side_effect = tool_side_effect
        self._gate = ApprovalGate(run)

    # One LLM call == one governed step. Raises BudgetExceeded when spent.
    def on_llm_start(self, serialized: Any, prompts: Any, **kwargs: Any) -> None:
        self.run.step()

    def on_chat_model_start(self, serialized: Any, messages: Any, **kwargs: Any) -> None:
        self.run.step()

    def on_tool_start(self, serialized: Any, input_str: Any, **kwargs: Any) -> None:
        if not self.gate_tools:
            return
        name = ""
        if isinstance(serialized, dict):
            name = serialized.get("name", "")
        self._gate.require(name or "tool", side_effect=self.tool_side_effect,
                           arguments={"input": _stringify(input_str)})


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
