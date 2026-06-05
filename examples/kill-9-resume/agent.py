#!/usr/bin/env python3
"""kill-9-resume — a crashed run resumes without re-spending. THE flagship.

An agent does expensive, checkpointed work under a governed run. The RiskKernel
daemon is `kill -9`'d mid-run — a hard crash, no graceful shutdown. On restart the
daemon reloads the run with the budget and usage it had already spent; re-running
this agent attaches to that same run (``resume_run``), reads its last checkpoint,
and finishes the work — WITHOUT redoing (re-paying for) the steps already done.

The proof: across the crash the governor's loop counter ends at exactly the number
of steps of *work* (10), not double (it never restarts from zero).

Run it twice with a daemon crash in between — or just run ``./demo.sh``, which
scripts the whole thing. This agent owns one piece of state: a file holding the
run id to resume (``$RK_RUN_ID_FILE``, default ``.resume-run-id``).
"""

from __future__ import annotations

import os
import time

import riskkernel as rk

DAEMON_URL = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
TOTAL_STEPS = int(os.environ.get("RK_TOTAL_STEPS", "10"))
WORK_SECONDS = float(os.environ.get("RK_WORK_SECONDS", "0.4"))
RUN_ID_FILE = os.environ.get("RK_RUN_ID_FILE", ".resume-run-id")
# Demo knob: stop cleanly after this many total steps (so the orchestrator can
# crash the daemon at a known point). 0 = run to completion.
STOP_AFTER = int(os.environ.get("RK_STOP_AFTER", "0")) or TOTAL_STEPS


def do_expensive_work(step: int) -> None:
    """Stand-in for the per-step work you don't want to pay for twice — a model
    call, a tool invocation, a long computation. Here it just sleeps."""
    time.sleep(WORK_SECONDS)


def run_loop(run: "rk.Run", start: int) -> int:
    """Do steps [start, TOTAL_STEPS), checkpointing each. Returns the next cursor."""
    i = start
    while i < TOTAL_STEPS:
        if i >= STOP_AFTER:
            print(f"\n⏸  did {i} steps, then stopping (the demo crashes the daemon here).")
            return i
        run.step()                                   # one governed step (counts vs the budget)
        do_expensive_work(i)
        run.checkpoint("progress", {"cursor": i + 1})  # save WHERE we are, durably
        print(f"  step {i + 1:>2}/{TOTAL_STEPS} done   (checkpointed cursor={i + 1})")
        i += 1
    return i


def _finish(run: "rk.Run") -> None:
    loops = run.status().get("usage", {}).get("loops")
    if os.path.exists(RUN_ID_FILE):
        os.remove(RUN_ID_FILE)
    print(f"\n✅ completed all {TOTAL_STEPS} steps. governor loop counter = {loops} "
          f"— exactly one per step of work. The steps finished before the crash were "
          f"neither redone nor re-paid.")


def main() -> int:
    rt = rk.Runtime(base_url=DAEMON_URL)
    try:
        if os.path.exists(RUN_ID_FILE):
            # ── RESUME path: attach to the existing run after the crash ──
            run_id = open(RUN_ID_FILE).read().strip()
            with rt.resume_run(run_id) as run:
                cp = run.latest_checkpoint()
                start = int(cp["payload"]["cursor"]) if cp and cp.get("payload") else 0
                spent = run.status().get("usage", {}).get("loops", start)
                print(f"↻ RESUMING run {run_id}\n"
                      f"  the governor already counts {spent} spent steps — resuming at "
                      f"cursor {start}, not redoing them.\n")
                end = run_loop(run, start)
                if end >= TOTAL_STEPS:
                    _finish(run)
        else:
            # ── FRESH path: open a new governed run ──
            budget = rt.budget(loops=TOTAL_STEPS * 5, seconds=3600)  # generous; the demo is about resume, not halt
            with rt.governed_run(name="kill-9-resume", budget=budget) as run:
                open(RUN_ID_FILE, "w").write(run.id)
                print(f"▶ FRESH run {run.id}   (budget: loops={TOTAL_STEPS * 5})\n")
                end = run_loop(run, 0)
                if end >= TOTAL_STEPS:
                    _finish(run)
        return 0
    except rk.APIError as e:
        if e.code == "connection_error":
            print("\n💥 the daemon is gone (kill -9?). Your progress is safe — it's "
                  "checkpointed in the run's SQLite state.\n"
                  "   Restart the daemon and re-run this script: it RESUMES from the last "
                  "checkpoint, no work re-paid.")
            return 1
        raise


if __name__ == "__main__":
    raise SystemExit(main())
