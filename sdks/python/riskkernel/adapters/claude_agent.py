"""Claude Agent SDK adapter: a PreToolUse hook that routes side-effecting tool
calls through the RiskKernel approval gate. Maps cleanly to the Claude Agent SDK's
permission model (``permissionDecision: "deny"`` blocks a tool).

    from riskkernel.adapters.claude_agent import make_pre_tool_use_hook
    hook = make_pre_tool_use_hook(run, side_effect_for={"Bash": "exec", "Write": "write"})
    # register `hook` as your PreToolUse hook in the Claude Agent SDK options.

The hook signature follows the Claude Agent SDK: it receives the hook input
(containing the tool name and input) and returns a decision dict. Because SDK
versions differ slightly, the returned shape is the documented
``hookSpecificOutput`` / ``permissionDecision`` form; adjust if your version
differs.
"""

from __future__ import annotations

from typing import Any, Callable, Dict, Optional

from ..runtime import Run


def make_pre_tool_use_hook(
    run: Run,
    side_effect_for: Optional[Dict[str, str]] = None,
    default_side_effect: str = "write",
    timeout: Optional[float] = None,
) -> Callable[..., dict]:
    """Build a PreToolUse hook bound to a governed run.

    Args:
        run: the governed Run.
        side_effect_for: map of tool name -> side-effect label. Tools not listed
            use ``default_side_effect``. A tool mapped to "" (empty) is treated as
            read-only and never gated.
        default_side_effect: side effect for unlisted tools.
        timeout: max seconds to await a human decision.
    """
    side_effect_for = side_effect_for or {}

    def hook(input_data: Any = None, *args: Any, **kwargs: Any) -> dict:
        tool_name, tool_input = _extract(input_data, kwargs)
        side_effect = side_effect_for.get(tool_name, default_side_effect)
        decision = run.approve(
            tool_name or "tool", side_effect=side_effect,
            arguments={"input": _stringify(tool_input)}, timeout=timeout,
        )
        if decision.approved:
            return {}  # allow (no decision == proceed)
        return {
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": decision.reason or "denied via RiskKernel approval gate",
            }
        }

    return hook


def _extract(input_data: Any, kwargs: dict):
    """Pull tool name + input out of the hook payload across SDK shapes."""
    data = input_data if isinstance(input_data, dict) else kwargs
    name = data.get("tool_name") or data.get("toolName") or data.get("name") or ""
    tinput = data.get("tool_input") or data.get("toolInput") or data.get("input")
    return name, tinput


def _stringify(v: Any) -> Any:
    try:
        import json
        json.dumps(v)
        return v
    except Exception:
        return repr(v)
