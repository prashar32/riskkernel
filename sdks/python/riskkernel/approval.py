"""Human-in-the-loop approval helpers for the SDK."""

from __future__ import annotations

import functools
from typing import Any, Callable, Optional

from .errors import ApprovalDenied, RiskKernelError
from .runtime import Decision, Run, current_run


class ApprovalGate:
    """Gates side-effecting actions on human approval. Wraps a Run and asks the
    daemon (deterministic policy) whether a call needs approval, then blocks until
    a human resolves it.

        gate = ApprovalGate(run)
        if gate.allow("mcp://shell", side_effect="exec", arguments={"cmd": cmd}):
            run_shell(cmd)
    """

    def __init__(self, run: Optional[Run] = None):
        self._run = run

    def _resolve_run(self) -> Run:
        run = self._run or current_run()
        if run is None:
            raise RiskKernelError("ApprovalGate used outside a governed run")
        return run

    def decide(self, tool: str, side_effect: str = "", arguments: Optional[dict] = None,
               step_index: int = 0, timeout: Optional[float] = None) -> Decision:
        """Return the Decision (blocking until resolved). Does not raise on denial."""
        return self._resolve_run().approve(
            tool, side_effect=side_effect, arguments=arguments,
            step_index=step_index, timeout=timeout,
        )

    def allow(self, tool: str, side_effect: str = "", arguments: Optional[dict] = None,
              step_index: int = 0, timeout: Optional[float] = None) -> bool:
        """Convenience boolean: True if approved."""
        return self.decide(tool, side_effect, arguments, step_index, timeout).approved

    def require(self, tool: str, side_effect: str = "", arguments: Optional[dict] = None,
                step_index: int = 0, timeout: Optional[float] = None) -> None:
        """Raise ApprovalDenied if not approved (use to guard before a side effect)."""
        d = self.decide(tool, side_effect, arguments, step_index, timeout)
        if not d.approved:
            raise ApprovalDenied(tool, d.reason)


def governed_tool(_fn: Optional[Callable] = None, *, tool: Optional[str] = None,
                  side_effect: str = "write", timeout: Optional[float] = None):
    """Decorator for a side-effecting tool function: before it runs, ask the
    approval gate (under the current governed run). Raises ApprovalDenied if the
    human says no.

        @governed_tool(side_effect="write")
        def write_file(path, content): ...
    """

    def decorate(fn: Callable) -> Callable:
        tool_name = tool or fn.__name__

        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            ApprovalGate().require(
                tool_name, side_effect=side_effect,
                arguments={"args": _safe(args), "kwargs": _safe(kwargs)},
                timeout=timeout,
            )
            return fn(*args, **kwargs)

        return wrapper

    return decorate(_fn) if _fn is not None else decorate


def _safe(obj: Any) -> Any:
    """Best-effort JSON-able rendering of call arguments for the approver to read."""
    try:
        import json
        json.dumps(obj)
        return obj
    except Exception:
        return repr(obj)
