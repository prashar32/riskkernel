"""AutoGen adapter: a model-client wrapper that enforces a governed run's loop and
time budgets, ticking one governed step per model request. Wrap your AutoGen model
client once and hand the wrapped client to your existing ``AssistantAgent`` (or
team) — no other code change — and the deterministic governor caps the run; point
the same client at the governing proxy (``run.proxy_config()``) for token/cost
metering, and this wrapper adds the outer-loop enforcement the proxy can't see.

    from autogen_agentchat.agents import AssistantAgent
    from autogen_ext.models.openai import OpenAIChatCompletionClient
    from riskkernel.adapters.autogen import GovernedChatCompletionClient

    client = GovernedChatCompletionClient(OpenAIChatCompletionClient(model="gpt-4o"), run)
    agent = AssistantAgent("assistant", model_client=client)
    await agent.run(task="...")          # one governed step per model call

Each ``create()`` / ``create_stream()`` call ticks ``run.step()`` *before*
delegating to the real client — one model request == one governed step — so a
runaway agent is hard-stopped at its loop/time budget. With ``gate_tools=True``,
the wrapper inspects the returned ``CreateResult`` for tool-call requests
(``FunctionCall``) and routes each through the approval gate before the result is
handed back to the agent, so a side-effecting tool the model wants to invoke is
blocked unless approved.

Propagation (verified against the library — note the asymmetry):

* **A single agent run directly** (``agent.run()`` / ``agent.on_messages()``) does
  NOT wrap model-client exceptions: a ``BudgetExceeded`` raised here propagates
  out *unwrapped*, halting the agent.
* **Inside a team** (``RoundRobinGroupChat`` / ``SelectorGroupChat`` /
  ``BaseGroupChat.run()`` / ``run_stream()``), the agent's container catches the
  exception, serializes it (``SerializableException``) onto the group-chat error
  channel, and the team re-raises it to the caller as a plain
  ``RuntimeError(str(error))`` — so the run still halts (it is NOT swallowed), but
  the original ``BudgetExceeded`` *type* is lost, replaced by a ``RuntimeError``
  whose message reads ``"BudgetExceeded: run halted: loop_budget_exceeded"``. Use
  ``governed_run_errors()`` (a context manager) around the team call to restore the
  typed ``BudgetExceeded`` / ``ApprovalDenied`` so callers can ``except`` on it::

      from riskkernel.adapters.autogen import governed_run_errors
      with governed_run_errors():
          await team.run(task="...")     # re-raises typed BudgetExceeded, not RuntimeError

Supported API: the autogen-core ``ChatCompletionClient`` protocol
(``autogen_core.models``) used by ``autogen-agentchat`` — i.e. the actively
maintained v0.4+ line (``autogen-agentchat`` / ``autogen-core`` >= 0.4), NOT the
legacy ``pyautogen`` 0.2 ``register_reply`` API. The wrapper delegates every
protocol method to the wrapped client, so it is a drop-in. ``autogen`` is NOT a
dependency of the SDK — nothing here imports it; the wrapper is duck-typed and the
module imports fine without AutoGen present.
"""

from __future__ import annotations

from typing import Any, Optional

from ..approval import ApprovalGate
from ..errors import ApprovalDenied, BudgetExceeded
from ..runtime import Run


class GovernedChatCompletionClient:
    """Wraps any AutoGen ``ChatCompletionClient`` and binds it to a governed run.

    Pass the wrapped client to your ``AssistantAgent(model_client=...)`` (or any
    agent that takes a model client). Each model request ticks one governed step,
    enforcing the run's loop/time budget; ``BudgetExceeded`` is raised from the
    governor and propagates out of the model call. Every other protocol method is
    delegated unchanged to the wrapped client.

    Args:
        client: the real AutoGen model client to wrap (an
            ``autogen_core.models.ChatCompletionClient``).
        run: the governed Run.
        gate_tools: if True, every tool call the model requests (a ``FunctionCall``
            in the ``CreateResult``) must pass the approval gate before the result
            is returned to the agent; a denial raises ``ApprovalDenied`` and halts.
        tool_side_effect: side-effect label reported for gated tools.
        timeout: max seconds to await a human decision on a gated tool.
    """

    def __init__(self, client: Any, run: Run, gate_tools: bool = False,
                 tool_side_effect: str = "tool", timeout: Optional[float] = None):
        self._client = client
        self.run = run
        self.gate_tools = gate_tools
        self.tool_side_effect = tool_side_effect
        self.timeout = timeout
        self._gate = ApprovalGate(run)

    # One model request == one governed step. run.step() raises BudgetExceeded when
    # the loop/time budget is spent; AutoGen does not catch it inside the agent, so
    # the halt propagates out of the model call (and out of agent.run()). Inside a
    # team it is surfaced as a RuntimeError — see governed_run_errors().
    async def create(self, *args: Any, **kwargs: Any) -> Any:
        self.run.step()
        result = await self._client.create(*args, **kwargs)
        if self.gate_tools:
            self._gate_result_tools(result)
        return result

    async def create_stream(self, *args: Any, **kwargs: Any):
        # Tick the step before the stream opens (same as the non-streaming path), so
        # a runaway loop is capped before another model request begins. The final
        # chunk of an AutoGen model stream is the CreateResult; gate its tool calls.
        self.run.step()
        async for chunk in self._client.create_stream(*args, **kwargs):
            if self.gate_tools and _is_create_result(chunk):
                self._gate_result_tools(chunk)
            yield chunk

    def _gate_result_tools(self, result: Any) -> None:
        """Route each tool call the model requested through the approval gate.

        A ``CreateResult.content`` is either a string (plain text — no tools) or a
        list of ``FunctionCall`` objects (each with ``.name`` / ``.arguments``).
        Gate each before the agent gets the chance to execute it. Duck-typed so this
        works without importing AutoGen."""
        for call in _tool_calls(result):
            self._gate.require(
                _call_name(call) or "tool",
                side_effect=self.tool_side_effect,
                arguments={"arguments": _stringify(_call_arguments(call))},
                timeout=self.timeout,
            )

    # ── Delegate the rest of the ChatCompletionClient protocol to the wrapped
    # client unchanged, so this stays a drop-in replacement across AutoGen versions.
    def actual_usage(self) -> Any:
        return self._client.actual_usage()

    def total_usage(self) -> Any:
        return self._client.total_usage()

    def count_tokens(self, *args: Any, **kwargs: Any) -> Any:
        return self._client.count_tokens(*args, **kwargs)

    def remaining_tokens(self, *args: Any, **kwargs: Any) -> Any:
        return self._client.remaining_tokens(*args, **kwargs)

    async def close(self) -> None:
        await self._client.close()

    @property
    def model_info(self) -> Any:
        return self._client.model_info

    @property
    def capabilities(self) -> Any:
        return self._client.capabilities

    def __getattr__(self, name: str) -> Any:
        # Any protocol method or attribute not named above (AutoGen adds/renames
        # some across versions) falls through to the wrapped client. __getattr__ is
        # only consulted for names not found normally, so it never shadows the
        # governed create()/create_stream() above.
        return getattr(self._client, name)


class governed_run_errors:
    """Context manager that restores the typed RiskKernel exception when a governed
    halt happens *inside an AutoGen team*.

    A team (``RoundRobinGroupChat`` etc.) catches an agent's exception and re-raises
    it to the caller as a plain ``RuntimeError`` whose message is
    ``"BudgetExceeded: <message>"`` (the original type name + message). This wraps
    the team call and re-raises the original typed ``BudgetExceeded`` /
    ``ApprovalDenied`` so callers can ``except`` on it as they do everywhere else::

        with governed_run_errors():
            await team.run(task="...")

    A single agent run directly doesn't need this (it propagates the typed exception
    already), but it is harmless there. Only RuntimeErrors that match a known
    RiskKernel error prefix are converted; any other RuntimeError is left untouched.
    """

    def __enter__(self) -> "governed_run_errors":
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> bool:
        if exc is None or not isinstance(exc, RuntimeError):
            return False
        typed = _typed_from_runtime_error(exc)
        if typed is None:
            return False
        raise typed from exc


def _typed_from_runtime_error(exc: RuntimeError) -> Optional[Exception]:
    """Map an AutoGen-wrapped team RuntimeError back to its RiskKernel type.

    AutoGen's SerializableException stringifies as ``"<ErrorType>: <message>"`` (and
    may append a traceback on following lines), so the first line begins with the
    original exception's class name. Match on that prefix to reconstruct the typed
    error; return None if it isn't one of ours (leave the RuntimeError alone)."""
    text = str(exc)
    first = text.split("\n", 1)[0]
    if first.startswith("BudgetExceeded:"):
        message = first[len("BudgetExceeded:"):].strip()
        # The governor's message is "run halted: <reason>"; recover the reason.
        reason = message.split("run halted:", 1)[-1].strip() if "run halted:" in message else message
        return BudgetExceeded(reason or "budget_exceeded", message)
    if first.startswith("ApprovalDenied:"):
        message = first[len("ApprovalDenied:"):].strip()
        # The message is "approval denied for <tool>[: <reason>]"; recover the tool.
        tool = message
        reason = ""
        if message.startswith("approval denied for "):
            rest = message[len("approval denied for "):]
            if ": " in rest:
                tool, reason = rest.split(": ", 1)
            else:
                tool = rest
        return ApprovalDenied(tool.strip() or "tool", reason.strip())
    return None


def _is_create_result(chunk: Any) -> bool:
    """True if a stream chunk is the final CreateResult (has ``.content`` and
    ``.finish_reason``); intermediate chunks are plain strings."""
    return hasattr(chunk, "content") and hasattr(chunk, "finish_reason")


def _tool_calls(result: Any) -> list:
    """The list of FunctionCall objects in a CreateResult, or [] for a text result.
    Duck-typed: tool calls are a list under ``.content``; a string is plain text."""
    content = getattr(result, "content", None)
    if isinstance(content, list):
        return [c for c in content if _is_tool_call(c)]
    return []


def _is_tool_call(obj: Any) -> bool:
    """True if obj looks like an AutoGen FunctionCall (has a ``.name``)."""
    return hasattr(obj, "name") and not isinstance(obj, str)


def _call_name(call: Any) -> str:
    name = getattr(call, "name", None)
    return str(name) if name else ""


def _call_arguments(call: Any) -> Any:
    return getattr(call, "arguments", None)


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
