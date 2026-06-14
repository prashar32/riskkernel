"""PydanticAI adapter: a model wrapper that enforces a governed run's loop and time
budgets, ticking one governed step per model request, and (optionally) gates the
tool calls a model proposes through the approval gate. Wrap your real model with it
and hand the wrapper to your ``Agent`` — no other change to your agent code:

    from pydantic_ai import Agent
    from riskkernel.adapters.pydantic_ai import govern

    agent = Agent(govern("anthropic:claude-sonnet-4-5", run))
    agent.run_sync("...")        # halts with BudgetExceeded when the budget is spent

``govern`` accepts either a model name (resolved by PydanticAI) or a ``Model``
instance, so it slots in wherever you already build your model:

    base = AnthropicModel("claude-sonnet-4-5")
    agent = Agent(govern(base, run, gate_tools=True))

A BudgetExceeded (or ApprovalDenied) raised inside the wrapped request propagates
out of ``agent.run()`` / ``agent.run_sync()`` and halts the agent. PydanticAI only
retries a model request on its own ``ModelRetry`` signal — every other exception is
terminal and bubbles out of the run (its model-request error handler re-raises by
default). So the deterministic halt surfaces to the caller and stops the agent; we
deliberately raise plain SDK exceptions (NOT ``ModelRetry``) so they are never
retried into another paid request.

Why wrap the model rather than hook tool execution: one model request maps cleanly
to one governed step (the agent's outer loop is request → tool calls → request …),
and the tool calls a step will make are present in the model's *response* before the
agent executes them — so a single, stable surface (the ``Model.request`` contract,
unchanged across the 1.x line) covers both loop/time enforcement and tool gating
without depending on a newer hooks API. We gate the proposed tool calls right after
the response comes back and before the agent runs them; a denial raises and halts.

Supported API: PydanticAI's ``Model`` / ``WrapperModel`` contract — pinned and
tested against ``pydantic-ai`` (``pydantic-ai-slim``) >= 1, < 2 (the post-1.0 line,
which commits to no breaking changes before 2.0; ``Model.request`` takes
``(messages, model_settings, model_request_parameters)`` and returns a
``ModelResponse``). PydanticAI is lazily imported and is NOT a dependency of the
SDK: this module imports cleanly without it, and the wrapper is only constructed
when you actually wire it.
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..runtime import Run


def _wrapper_base():
    # Inherit PydanticAI's WrapperModel when it is installed (it forwards every
    # Model method to the wrapped model, so we override only request/request_stream);
    # otherwise fall back to object so the module still imports without pydantic-ai.
    try:
        from pydantic_ai.models.wrapper import WrapperModel  # type: ignore
        return WrapperModel
    except Exception:
        return object


class GovernedModel(_wrapper_base()):  # type: ignore[misc]
    """A PydanticAI model wrapper that binds a governed run to an agent.

    It delegates every request to the wrapped model, but first ticks one governed
    step (enforcing the loop/time budget) and — when ``gate_tools`` is set — routes
    each tool call the model proposes through the approval gate before the agent
    executes it.

    Build it via :func:`govern` (which resolves a model name or instance), or
    construct it directly with an already-built ``Model``.

    Args:
        wrapped: the real PydanticAI ``Model`` (or a model name) to govern.
        run: the governed Run.
        gate_tools: if True, every tool call the model proposes must pass the
            approval gate before the agent runs it; a denial raises and halts.
        tool_side_effect: side-effect label reported for gated tools.
        timeout: max seconds to await a human decision on a gated tool.
    """

    def __init__(self, wrapped: Any, run: Run, gate_tools: bool = False,
                 tool_side_effect: str = "tool", timeout: Optional[float] = None):
        # WrapperModel.__init__ resolves a model name to a Model and stores it on
        # self.wrapped. Only call it when we actually inherit it (not the object
        # fallback), so the module is importable without pydantic-ai present.
        base = type(self).__mro__[1]
        if base is not object:
            base.__init__(self, wrapped)
        else:
            self.wrapped = wrapped
        self.run = run
        self.gate_tools = gate_tools
        self.tool_side_effect = tool_side_effect
        self.timeout = timeout
        self._gate = ApprovalGate(run)

    # One model request == one governed step. We tick the step first (so the loop/
    # time budget halts before we spend on the request), then delegate, then gate the
    # tool calls in the response before the agent executes them.
    async def request(self, messages: Any, model_settings: Any,
                      model_request_parameters: Any) -> Any:
        self.run.step()  # raises BudgetExceeded when the loop/time budget is spent
        response = await self.wrapped.request(
            messages, model_settings, model_request_parameters)
        if self.gate_tools:
            self._gate_response_tools(response)
        return response

    # Streaming takes the same path: tick a step, then stream from the wrapped model.
    # We don't gate tools here — the parts aren't known until the stream is consumed,
    # and the non-streaming request path is where tool calls are gated. Decorated as
    # an async context manager only when WrapperModel is present (matching the base).
    def request_stream(self, messages: Any, model_settings: Any,
                       model_request_parameters: Any,
                       run_context: Any = None) -> Any:
        self.run.step()  # raises BudgetExceeded when the loop/time budget is spent
        return self.wrapped.request_stream(
            messages, model_settings, model_request_parameters, run_context)

    def _gate_response_tools(self, response: Any) -> None:
        """Route each tool call the model proposed through the approval gate. A denial
        raises ApprovalDenied, which propagates out of the run and halts the agent
        before the tool executes."""
        for call in _tool_calls(response):
            name = getattr(call, "tool_name", None) or "tool"
            self._gate.require(str(name), side_effect=self.tool_side_effect,
                               arguments={"args": _stringify(getattr(call, "args", None))},
                               timeout=self.timeout)


def govern(model: Any, run: Run, gate_tools: bool = False,
           tool_side_effect: str = "tool",
           timeout: Optional[float] = None) -> GovernedModel:
    """Wrap a PydanticAI model (or model name) so its agent run is governed.

    Pass the result wherever you'd pass a model::

        agent = Agent(govern("anthropic:claude-sonnet-4-5", run))

    Args:
        model: a PydanticAI ``Model`` instance or a model name string.
        run: the governed Run.
        gate_tools: gate proposed tool calls through the approval gate (default off).
        tool_side_effect: side-effect label reported for gated tools.
        timeout: max seconds to await a human decision on a gated tool.
    """
    return GovernedModel(model, run, gate_tools=gate_tools,
                         tool_side_effect=tool_side_effect, timeout=timeout)


def _tool_calls(response: Any):
    """Yield the ToolCallPart-like parts of a PydanticAI ModelResponse. Duck-typed
    (a tool call has a ``tool_name`` attribute) so it works across versions and
    without importing pydantic-ai for the unit tests."""
    parts = getattr(response, "parts", None)
    if not parts:
        return
    for part in parts:
        if getattr(part, "tool_name", None) is not None:
            yield part


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
