#!/usr/bin/env python3
"""A governed agent loop, scaffolded by `riskkernel init`.

A deliberately runaway loop that RiskKernel's deterministic governor hard-stops at
its budget. No API key needed — the loop cap is enforced in the Go daemon, so you
can watch the kill with nothing running but `riskkernel serve`.

    riskkernel serve        # in another terminal
    python quickstart.py
"""

import riskkernel as rk

rt = rk.Runtime()  # talks to the daemon at http://localhost:7070

with rt.governed_run(name="quickstart", budget=rt.budget(loops=8)) as run:
    print(f"run {run.id} — budget: loops=8\n")
    step = 0
    try:
        while True:                       # a loop that would never stop on its own
            run.step()                    # raises rk.BudgetExceeded at the cap
            step += 1
            print(f"  step {step}")
    except rk.BudgetExceeded as halt:
        print(f"\nRiskKernel stopped it — {halt.reason}. The governor capped the loop at 8;")
        print("the loop never decided to stop. Swap the budget, wrap your own agent, and go.")
