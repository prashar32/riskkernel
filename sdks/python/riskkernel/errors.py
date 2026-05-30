"""Exceptions raised by the RiskKernel SDK."""

from __future__ import annotations


class RiskKernelError(Exception):
    """Base class for all SDK errors."""


class APIError(RiskKernelError):
    """The daemon returned an unexpected (non-2xx) response."""

    def __init__(self, status: int, code: str = "", message: str = ""):
        self.status = status
        self.code = code
        self.message = message
        super().__init__(f"riskkernel API error {status} {code}: {message}")


class BudgetExceeded(RiskKernelError):
    """A governed run hit one of its hard budgets (the deterministic governor
    halted it). ``reason`` is the machine-readable HaltReason, e.g.
    ``token_budget_exceeded`` or ``loop_budget_exceeded``."""

    def __init__(self, reason: str, message: str = ""):
        self.reason = reason
        super().__init__(message or f"run halted: {reason}")


class ApprovalDenied(RiskKernelError):
    """A human denied a side-effecting tool call gated by the approval gate."""

    def __init__(self, tool: str, reason: str = ""):
        self.tool = tool
        self.reason = reason
        super().__init__(f"approval denied for {tool}" + (f": {reason}" if reason else ""))


class ApprovalTimeout(RiskKernelError):
    """No human resolved a pending approval within the configured timeout."""
