#!/usr/bin/env python3
"""autogen — stop a runaway AutoGen agent at its budget.

Wraps an AutoGen model client with RiskKernel's ``GovernedChatCompletionClient``:
one governed step per model request. A deliberately runaway agent — one whose model
always asks to call a tool, so it never finishes on its own — is hard-stopped by the
deterministic governor at its loop budget, and the halt propagates out of
``agent.run()``. The kill comes from RiskKernel, not from this script.

Key-free: the model client here is a tiny local stub that always returns the same
tool call, so the loop enforcement runs with nothing but ``riskkernel serve`` — no
model call, no spend. To add the dollar / token ceiling with a *real* model, build
your real client (e.g. ``OpenAIChatCompletionClient``) pointed at the run's proxy
and wrap that instead; the README shows the few extra lines.

Run it:
    riskkernel serve            # in another terminal — no key needed for this demo
    pip install -r requirements.txt
    python agent.py

The integration is one object: ``GovernedChatCompletionClient(model_client, run)``.
Hand the wrapped client to your existing ``AssistantAgent`` and the governor caps
the loop the same way it would cap a real agent.
"""

from __future__ import annotations

import asyncio
import os

import riskkernel as rk
from riskkernel.adapters.autogen import GovernedChatCompletionClient

try:
    from autogen_agentchat.agents import AssistantAgent
    from autogen_core import CancellationToken, FunctionCall
    from autogen_core.models import (
        ChatCompletionClient,
        CreateResult,
        ModelInfo,
        RequestUsage,
    )
    from autogen_core.tools import FunctionTool
except ImportError:
    raise SystemExit(
        "this example needs autogen-agentchat + autogen-core — install them with:\n"
        "    pip install -r requirements.txt"
    )

# ─────────────────────────────────────────────────────────────────────────────
# Knobs. The kill is never faked: the governor (in the daemon) enforces the loop
# budget before each model request and the wrapper propagates the halt into AutoGen.
# ─────────────────────────────────────────────────────────────────────────────
DAEMON_URL = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
LOOP_BUDGET = 6        # max model requests (loop iterations) the governor allows


def _noop(query: str) -> str:
    """A do-nothing tool the agent keeps calling."""
    return "keep going"


class _LoopingClient(ChatCompletionClient):
    """A stand-in model client so the demo needs no key: it always returns the same
    tool call, so the agent loops forever — until the governor caps it. Swap this for
    a real ``OpenAIChatCompletionClient`` / ``AnthropicChatCompletionClient`` pointed
    at ``run.proxy_config()`` to add the dollar/token ceiling (see the README)."""

    def __init__(self) -> None:
        self._usage = RequestUsage(prompt_tokens=0, completion_tokens=0)

    async def create(self, messages, *, tools=[], tool_choice="auto",
                     json_output=None, extra_create_args={}, cancellation_token=None):
        return CreateResult(
            finish_reason="function_calls",
            content=[FunctionCall(id="1", name="noop", arguments='{"query": "again"}')],
            usage=RequestUsage(prompt_tokens=1, completion_tokens=1),
            cached=False,
        )

    async def create_stream(self, *args, **kwargs):  # pragma: no cover
        raise NotImplementedError

    async def close(self) -> None:
        return None

    def actual_usage(self):
        return self._usage

    def total_usage(self):
        return self._usage

    def count_tokens(self, messages, *, tools=[]):
        return 0

    def remaining_tokens(self, messages, *, tools=[]):
        return 1000

    @property
    def capabilities(self):
        return self.model_info

    @property
    def model_info(self):
        return ModelInfo(vision=False, function_calling=True, json_output=False,
                         family="unknown", structured_output=False)


async def _run() -> int:
    rt = rk.Runtime(base_url=DAEMON_URL)
    print(f"▶ autogen   loop budget = {LOOP_BUDGET}   (enforced by the Go governor)")

    tool = FunctionTool(_noop, description="a no-op tool the agent keeps calling")

    try:
        with rt.governed_run(name="autogen-demo",
                             budget=rt.budget(loops=LOOP_BUDGET)) as run:
            print(f"  run id: {run.id}\n")
            # The one integration line: wrap the model client. Everything below is a
            # plain AutoGen agent — no other change.
            client = GovernedChatCompletionClient(_LoopingClient(), run)
            agent = AssistantAgent(
                "looper", model_client=client, tools=[tool],
                reflect_on_tool_use=False, max_tool_iterations=1000,
            )
            try:
                await agent.run(task="keep using the tool",
                                cancellation_token=CancellationToken())
            except rk.BudgetExceeded as halt:
                _report_halt(run, halt)
                return 0
            print("  (agent finished on its own before the budget — unexpected here)")
            return 0
    except rk.APIError as e:
        if e.code == "connection_error":
            print(f"\n✗ can't reach the daemon at {DAEMON_URL} — start it first:")
            print("    riskkernel serve")
            print("    # or: docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest")
            return 1
        raise


def _report_halt(run: rk.Run, halt: rk.BudgetExceeded) -> None:
    """Print the final, governor-enforced ledger for the halted AutoGen run."""
    usage = run.status().get("usage", {})
    print(f"\n🛑 RiskKernel halted the AutoGen run — reason: {halt.reason}")
    print("   ── final ledger (enforced by the governor) ──")
    print(f"     model calls (loops) : {usage.get('loops', 0):>4}   (budget: {LOOP_BUDGET})")
    print(f"     run id              : {run.id}")
    print("   The agent would have looped forever; the governor capped it — and the")
    print("   halt propagated out of agent.run().")


def main() -> int:
    return asyncio.run(_run())


if __name__ == "__main__":
    raise SystemExit(main())
