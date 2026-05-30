"""High-level governed-run ergonomics over the thin client.

The runtime is still thin: budgets, loop/time enforcement, checkpoints, and
approval decisions all happen in the Go daemon. This module just makes them
pleasant to use from Python (context managers, decorators, a current-run var).
"""

from __future__ import annotations

import contextvars
import functools
import os
import time
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any, Callable, Optional

from .client import RiskKernel
from .errors import ApprovalTimeout

# The run currently in scope (set by governed_run), so @governed_tool and
# checkpoint() can find it without threading it through every call.
_current_run: contextvars.ContextVar[Optional["Run"]] = contextvars.ContextVar(
    "riskkernel_current_run", default=None
)


def current_run() -> Optional["Run"]:
    """Return the governed run currently in scope, or None."""
    return _current_run.get()


@dataclass
class Budget:
    """Hard per-run limits. Any field left None is unlimited for that dimension."""

    tokens: Optional[int] = None
    dollars: Optional[float] = None
    loops: Optional[int] = None
    seconds: Optional[int] = None

    def to_dict(self) -> dict:
        out: dict = {}
        if self.tokens is not None:
            out["tokens"] = self.tokens
        if self.dollars is not None:
            out["dollars"] = self.dollars
        if self.loops is not None:
            out["loops"] = self.loops
        if self.seconds is not None:
            out["seconds"] = self.seconds
        return out


@dataclass
class Decision:
    approved: bool
    required: bool = True
    reason: str = ""
    by: str = ""


class Run:
    """A governed run bound to a daemon run id."""

    def __init__(self, client: RiskKernel, data: dict,
                 poll_interval: float = 2.0, timeout: Optional[float] = None):
        self._client = client
        self._poll = poll_interval
        self._timeout = timeout
        self.id: str = data["id"]
        self.data = data

    def step(self) -> int:
        """Register a loop iteration; raises BudgetExceeded if loop/time budget is spent."""
        return self._client.begin_step(self.id)

    def checkpoint(self, name: str = "", payload: Optional[dict] = None) -> None:
        self._client.checkpoint(self.id, name, payload)

    def latest_checkpoint(self) -> Optional[dict]:
        return self._client.latest_checkpoint(self.id)

    def cancel(self, reason: str = "") -> dict:
        return self._client.cancel(self.id, reason)

    def status(self) -> dict:
        return self._client.get_run(self.id)

    def proxy_config(self) -> dict:
        """Config for routing this run's model calls through the governing proxy.
        Point your LLM client's base URL here and send the header so every call is
        metered, priced, and budget-enforced under this run."""
        return {
            "base_url": self._client.base_url + "/v1",
            "headers": {"X-RiskKernel-Run-Id": self.id},
        }

    def approve(self, tool: str, side_effect: str = "", arguments: Optional[dict] = None,
                step_index: int = 0, poll_interval: Optional[float] = None,
                timeout: Optional[float] = None) -> Decision:
        """Request approval for a tool call, blocking (polling) until a human
        resolves it. Returns a Decision; raises ApprovalTimeout if none arrives."""
        res = self._client.request_approval(self.id, tool, side_effect, arguments, step_index)
        if res.get("status") == "approved":
            return Decision(True, bool(res.get("required", True)))
        approval_id = res["id"]
        interval = poll_interval if poll_interval is not None else self._poll
        limit = timeout if timeout is not None else self._timeout
        deadline = (time.monotonic() + limit) if limit is not None else None
        while True:
            a = self._client.get_approval(approval_id)
            st = a.get("status")
            if st == "approved":
                return Decision(True, True, a.get("reason", ""), a.get("decidedBy", ""))
            if st == "denied":
                return Decision(False, True, a.get("reason", ""), a.get("decidedBy", ""))
            if deadline is not None and time.monotonic() > deadline:
                raise ApprovalTimeout(f"no decision for approval {approval_id} within {limit}s")
            time.sleep(interval)


class Runtime:
    """Entry point: holds a client and default approval-polling settings."""

    def __init__(self, client: Optional[RiskKernel] = None,
                 base_url: str = "http://localhost:7070", token: Optional[str] = None,
                 approval_poll_interval: float = 2.0,
                 approval_timeout: Optional[float] = None):
        self.client = client or RiskKernel(base_url, token)
        self._poll = approval_poll_interval
        self._timeout = approval_timeout

    def budget(self, tokens: Optional[int] = None, dollars: Optional[float] = None,
               loops: Optional[int] = None, seconds: Optional[int] = None) -> Budget:
        return Budget(tokens, dollars, loops, seconds)

    @contextmanager
    def governed_run(self, name: Optional[str] = None,
                     budget: Optional[Budget | dict] = None,
                     metadata: Optional[dict] = None, cancel_on_error: bool = True):
        """Context manager that opens a governed run, sets it as the current run,
        and cancels it if the body raises (unless cancel_on_error=False)."""
        b = budget.to_dict() if isinstance(budget, Budget) else budget
        data = self.client.create_run(name=name, budget=b, metadata=metadata)
        run = Run(self.client, data, self._poll, self._timeout)
        token = _current_run.set(run)
        try:
            yield run
        except Exception:
            if cancel_on_error:
                try:
                    run.cancel("error")
                except Exception:
                    pass
            raise
        finally:
            _current_run.reset(token)


# Module-level default runtime, configured from the environment, for the
# decorator/convenience API.
_default_runtime: Optional[Runtime] = None


def default_runtime() -> Runtime:
    global _default_runtime
    if _default_runtime is None:
        _default_runtime = Runtime(
            base_url=os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070"),
            token=os.environ.get("RISKKERNEL_API_TOKEN"),
        )
    return _default_runtime


def configure(runtime: Runtime) -> None:
    """Override the module-level default runtime (used by the decorators)."""
    global _default_runtime
    _default_runtime = runtime


def governed_run(_fn: Optional[Callable] = None, *, name: Optional[str] = None,
                 budget: Optional[Budget | dict] = None, runtime: Optional[Runtime] = None):
    """Decorator: run the wrapped function inside a governed run. The run is
    available via current_run() (and passed as the ``run`` kwarg if the function
    declares one)."""

    def decorate(fn: Callable) -> Callable:
        @functools.wraps(fn)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            rt = runtime or default_runtime()
            run_name = name or fn.__name__
            with rt.governed_run(name=run_name, budget=budget) as run:
                if "run" in fn.__code__.co_varnames and "run" not in kwargs:
                    kwargs["run"] = run
                return fn(*args, **kwargs)
        return wrapper

    return decorate(_fn) if _fn is not None else decorate
