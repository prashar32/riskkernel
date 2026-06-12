"""RiskKernel Python SDK — Surface 2 (deep control).

A thin client over the self-hosted RiskKernel daemon. The Go core makes every
deterministic decision (budgets, halts, approval policy); this package just makes
governed runs ergonomic from Python.

Quickstart::

    import riskkernel as rk

    rt = rk.Runtime(base_url="http://localhost:7070")
    with rt.governed_run(name="research", budget=rt.budget(dollars=1.00, loops=20)) as run:
        cfg = run.proxy_config()   # route your LLM client through the governing proxy
        for _ in range(100):
            run.step()             # raises BudgetExceeded when loops/time run out
            ...                    # your agent reasoning + tool calls
            run.checkpoint("after-step", {"messages": messages})
"""

from .client import RiskKernel
from .errors import (
    APIError,
    ApprovalDenied,
    ApprovalTimeout,
    BudgetExceeded,
    RiskKernelError,
)
from .approval import ApprovalGate, governed_tool
from .runtime import (
    Budget,
    Decision,
    Run,
    Runtime,
    configure,
    current_run,
    default_runtime,
    governed_run,
)

__version__ = "0.5.0"

__all__ = [
    "RiskKernel",
    "Runtime",
    "Run",
    "Budget",
    "Decision",
    "ApprovalGate",
    "governed_run",
    "governed_tool",
    "current_run",
    "configure",
    "default_runtime",
    "RiskKernelError",
    "APIError",
    "BudgetExceeded",
    "ApprovalDenied",
    "ApprovalTimeout",
]
