#!/usr/bin/env python3
"""langchain — stop a runaway LangChain agent at its budget.

Wraps a LangChain LLM loop with RiskKernel's callback handler: one governed step
per model call. A deliberately runaway loop is hard-stopped by the deterministic
governor at its loop budget — and the halt propagates out of ``llm.invoke()``,
ending the chain. The kill comes from RiskKernel, not from this script.

Key-free: this uses LangChain's ``FakeListLLM`` so the loop enforcement runs with
nothing but ``riskkernel serve`` — no model call, no spend. To add the dollar /
token ceiling with a *real* model, route the LLM through the run's proxy; the
README shows the few extra lines.

Run it:
    riskkernel serve            # in another terminal — no key needed for this demo
    pip install -r requirements.txt
    python agent.py

The integration is two objects: ``RiskKernelCallbackHandler(run)`` and passing it
as a LangChain callback. The handler ticks one governed step per LLM call, so the
governor caps the loop the same way it would cap a real agent.
"""

from __future__ import annotations

import logging
import os

import riskkernel as rk
from riskkernel.adapters.langchain import RiskKernelCallbackHandler

# LangChain logs any exception raised inside a callback as an "error" — even the
# BudgetExceeded we raise on purpose to stop the run. Quiet just that logger so the
# demo output stays clean; the halt is intentional and handled below.
logging.getLogger("langchain_core.callbacks.manager").setLevel(logging.CRITICAL)

try:
    from langchain_core.language_models.fake import FakeListLLM
except ImportError:
    raise SystemExit(
        "this example needs langchain-core — install it with:\n"
        "    pip install -r requirements.txt"
    )

# ─────────────────────────────────────────────────────────────────────────────
# Knobs. The kill is never faked: the governor (in the daemon) enforces the loop
# budget before each LLM call and the handler propagates the halt into LangChain.
# ─────────────────────────────────────────────────────────────────────────────
DAEMON_URL = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
LOOP_BUDGET = 6        # max LLM calls (loop iterations) the governor allows


def main() -> int:
    rt = rk.Runtime(base_url=DAEMON_URL)
    print(f"▶ langchain   loop budget = {LOOP_BUDGET}   (enforced by the Go governor)")

    # A stand-in model so the demo needs no key. Swap FakeListLLM for
    # ChatAnthropic / ChatOpenAI pointed at run.proxy_config() to add the
    # dollar/token ceiling on real calls — see the README.
    llm = FakeListLLM(responses=["…thinking; I'll keep going…"] * 1000)

    try:
        with rt.governed_run(name="langchain-demo",
                             budget=rt.budget(loops=LOOP_BUDGET)) as run:
            print(f"  run id: {run.id}\n")
            handler = RiskKernelCallbackHandler(run)
            step = 0
            while True:                           # a deliberately runaway agent loop
                try:
                    llm.invoke(f"step {step + 1}: decide the next action",
                               config={"callbacks": [handler]})
                except rk.BudgetExceeded as halt:
                    _report_halt(run, halt)
                    return 0
                step += 1
                print(f"  step {step:>2} │ LLM call allowed by the governor")
    except rk.APIError as e:
        if e.code == "connection_error":
            print(f"\n✗ can't reach the daemon at {DAEMON_URL} — start it first:")
            print("    riskkernel serve")
            print("    # or: docker run --rm -p 7070:7070 ghcr.io/prashar32/riskkernel:latest")
            return 1
        raise


def _report_halt(run: rk.Run, halt: rk.BudgetExceeded) -> None:
    """Print the final, governor-enforced ledger for the halted LangChain run."""
    usage = run.status().get("usage", {})
    print(f"\n🛑 RiskKernel halted the LangChain run — reason: {halt.reason}")
    print("   ── final ledger (enforced by the governor) ──")
    print(f"     LLM calls (loops) : {usage.get('loops', 0):>4}   (budget: {LOOP_BUDGET})")
    print(f"     run id            : {run.id}")
    print("   The agent would have looped forever; the governor capped it — and the")
    print("   halt propagated out of llm.invoke(), ending the chain.")


if __name__ == "__main__":
    raise SystemExit(main())
