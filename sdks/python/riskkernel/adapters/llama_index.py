"""LlamaIndex adapter: a callback handler that enforces a governed run's loop and
time budgets, ticking one step per LLM call. Register it on your LlamaIndex
``CallbackManager`` (or ``Settings.callback_manager``) and point your LlamaIndex
LLM at the governing proxy (``run.proxy_config()``) for token/cost/budget metering;
this handler adds the outer-loop enforcement the proxy can't see.

    from llama_index.core.callbacks import CallbackManager
    from llama_index.core import Settings
    from riskkernel.adapters.llama_index import RiskKernelCallbackHandler

    Settings.callback_manager = CallbackManager([RiskKernelCallbackHandler(run)])

A BudgetExceeded raised here propagates out of the LlamaIndex call, halting the
query/agent — exactly when the run is out of budget. Unlike LangChain, LlamaIndex's
``CallbackManager.on_event_start`` does NOT wrap handler calls in a try/except, so a
budget halt surfaces to the caller without any extra flag.

Supported API: LlamaIndex's ``BaseCallbackHandler`` callback protocol
(``llama-index-core`` >= 0.10, where the package split landed). We tick a step on
``CBEventType.LLM`` (``"llm"``) ``on_event_start`` — one LLM call == one governed
step — and, when ``gate_tools`` is set, gate ``CBEventType.FUNCTION_CALL``
(``"function_call"``) through the approval gate.
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..runtime import Run

# LlamaIndex event-type string values (CBEventType.LLM / .FUNCTION_CALL). We match
# on the string value so we don't need the enum imported when LlamaIndex is absent.
_LLM_EVENT = "llm"
_FUNCTION_CALL_EVENT = "function_call"


def _base_handler():
    # Inherit the real base class when LlamaIndex is installed (best integration);
    # otherwise fall back to object so the module still imports and the SDK installs
    # without llama-index present (it is NOT a dependency of the SDK).
    try:
        from llama_index.core.callbacks.base_handler import (  # type: ignore
            BaseCallbackHandler,
        )
        return BaseCallbackHandler
    except Exception:
        return object


class RiskKernelCallbackHandler(_base_handler()):  # type: ignore[misc]
    """Enforces loop/time budgets and (optionally) gates tools on approval.

    Args:
        run: the governed Run.
        gate_tools: if True, every tool/function call must pass the approval gate.
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
        # BaseCallbackHandler.__init__ requires the ignore lists. Call it only when
        # we actually inherit it (not the object fallback), so the module works
        # whether or not LlamaIndex is installed.
        base = type(self).__mro__[1]
        if base is not object:
            base.__init__(self, event_starts_to_ignore=[], event_ends_to_ignore=[])

    # One LLM call == one governed step; FUNCTION_CALL starts are gated when asked.
    # Returns the event_id unchanged — the callback manager uses it to correlate the
    # matching on_event_end, so we must not drop it.
    def on_event_start(self, event_type: Any, payload: Optional[dict] = None,
                       event_id: str = "", parent_id: str = "",
                       **kwargs: Any) -> str:
        et = _event_value(event_type)
        if et == _LLM_EVENT:
            self.run.step()  # raises BudgetExceeded when the loop/time budget is spent
        elif et == _FUNCTION_CALL_EVENT and self.gate_tools:
            name = _tool_name(payload)
            self._gate.require(name or "tool", side_effect=self.tool_side_effect,
                               arguments={"payload": _stringify(payload)},
                               timeout=self.timeout)
        return event_id

    def on_event_end(self, event_type: Any, payload: Optional[dict] = None,
                     event_id: str = "", **kwargs: Any) -> None:
        return None

    def start_trace(self, trace_id: Optional[str] = None) -> None:
        return None

    def end_trace(self, trace_id: Optional[str] = None,
                  trace_map: Optional[dict] = None) -> None:
        return None


def _event_value(event_type: Any) -> str:
    """Normalize a CBEventType (or its string value) to its lowercase string."""
    value = getattr(event_type, "value", event_type)
    return str(value).lower()


def _tool_name(payload: Optional[dict]) -> str:
    """Best-effort extraction of the tool name from a FUNCTION_CALL payload across
    LlamaIndex versions. The payload keys are EventPayload enum members whose string
    values are ``"tool"`` and ``"function_call"``; EventPayload.TOOL is typically a
    ToolMetadata with a ``.name``."""
    if not isinstance(payload, dict):
        return ""
    # Resolve by enum-string key, matching however the enum stringifies as a dict key.
    tool = None
    fn = None
    for key, val in payload.items():
        k = _event_value(key)
        if k == "tool":
            tool = val
        elif k == "function_call":
            fn = val
    if tool is not None:
        name = getattr(tool, "name", None)
        if name:
            return str(name)
        if isinstance(tool, dict):
            n = tool.get("name")
            if n:
                return str(n)
        if isinstance(tool, str):
            return tool
    if isinstance(fn, dict):
        n = fn.get("name") or fn.get("tool_name")
        if n:
            return str(n)
    return ""


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
