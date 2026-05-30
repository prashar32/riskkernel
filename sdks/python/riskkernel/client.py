"""Thin HTTP client for the RiskKernel daemon's /v1 API.

Stdlib only (urllib) — no third-party dependencies, so ``pip install riskkernel``
stays light and auditable. This client carries NO governance logic: the Go daemon
makes every deterministic decision. The client just relays calls and surfaces the
daemon's verdicts (e.g. a 402 becomes ``BudgetExceeded``).
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request
from typing import Any, Optional

from .errors import APIError, BudgetExceeded


class RiskKernel:
    """Client for a running RiskKernel daemon."""

    def __init__(
        self,
        base_url: str = "http://localhost:7070",
        token: Optional[str] = None,
        timeout: float = 30.0,
    ):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout

    # --- low-level ---

    def _request(self, method: str, path: str, body: Optional[dict] = None) -> Any:
        url = self.base_url + path
        data = None
        headers = {"Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token

        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else {}
        except urllib.error.HTTPError as e:
            raw = e.read()
            payload = {}
            try:
                payload = json.loads(raw)
            except Exception:
                pass
            code = payload.get("code", "")
            message = payload.get("message", e.reason or "")
            # 402 == the governor halted the run on a budget.
            if e.code == 402:
                raise BudgetExceeded(code, message) from None
            raise APIError(e.code, code, message) from None
        except urllib.error.URLError as e:
            raise APIError(0, "connection_error",
                           f"cannot reach daemon at {self.base_url}: {e.reason}") from None

    # --- runs ---

    def create_run(self, name: Optional[str] = None, budget: Optional[dict] = None,
                   metadata: Optional[dict] = None) -> dict:
        body: dict = {}
        if name:
            body["name"] = name
        if budget:
            body["budget"] = budget
        if metadata:
            body["metadata"] = metadata
        return self._request("POST", "/v1/runs", body)

    def get_run(self, run_id: str) -> dict:
        return self._request("GET", f"/v1/runs/{run_id}")

    def begin_step(self, run_id: str) -> int:
        """Register a loop iteration. Raises BudgetExceeded (402) if the loop or
        time budget is spent."""
        out = self._request("POST", f"/v1/runs/{run_id}/steps", {})
        return int(out.get("stepIndex", 0))

    def checkpoint(self, run_id: str, name: str = "", payload: Optional[dict] = None) -> None:
        self._request("POST", f"/v1/runs/{run_id}/checkpoints",
                      {"name": name, "payload": payload or {}})

    def latest_checkpoint(self, run_id: str) -> Optional[dict]:
        try:
            return self._request("GET", f"/v1/checkpoints/{run_id}")
        except APIError as e:
            if e.status == 404:
                return None
            raise

    def cancel(self, run_id: str, reason: str = "") -> dict:
        return self._request("POST", f"/v1/runs/{run_id}/cancel", {"reason": reason})

    # --- approvals ---

    def request_approval(self, run_id: str, tool: str, side_effect: str = "",
                         arguments: Optional[dict] = None, step_index: int = 0) -> dict:
        """Request approval for a (possibly side-effecting) tool call. Returns a
        dict with ``status``: ``approved`` (allowed by policy) or ``pending``
        (a human must decide; poll ``get_approval``)."""
        return self._request("POST", f"/v1/runs/{run_id}/approvals", {
            "tool": tool, "sideEffect": side_effect,
            "arguments": arguments or {}, "stepIndex": step_index,
        })

    def get_approval(self, approval_id: str) -> dict:
        return self._request("GET", f"/v1/approvals/{approval_id}")

    def resolve_approval(self, run_id: str, approval_id: str, approve: bool,
                         reason: str = "", decided_by: str = "sdk") -> dict:
        return self._request("POST", f"/v1/runs/{run_id}/approve", {
            "approvalId": approval_id,
            "decision": "approve" if approve else "deny",
            "reason": reason, "decidedBy": decided_by,
        })
