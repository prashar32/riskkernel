# kill-9-resume — a crashed run resumes without re-spending

**The flagship.** An agent does expensive, checkpointed work under a governed run.
The RiskKernel daemon is **`kill -9`'d mid-run** — a hard crash, no graceful
shutdown. On restart the daemon reloads the run with the budget and usage it had
already spent; re-running the agent attaches to that same run (`resume_run`), reads
its last checkpoint, and finishes the work — **without redoing (re-paying for) the
steps already done.**

The proof is one number: across the crash the governor's loop counter ends at
**10** (one per step of work), not 15 — it never restarts from zero, and never
re-spends.

## Run it (one command)

`demo.sh` scripts the whole thing reproducibly: start daemon → 5 of 10 steps →
`kill -9` the daemon → restart → resume → finish → show the proof.

```bash
cd examples/kill-9-resume
pip install -r requirements.txt           # the RiskKernel SDK (stdlib-only)

# needs the daemon binary + a python with the SDK:
RISKKERNEL_BIN=../../riskkernel ./demo.sh
#   (or, with the CLI on your PATH:  ./demo.sh)
```

## What you'll see

```
── 2. agent does 5 of 10 steps, checkpointing each ────────────────────
▶ FRESH run 4e25ad71-…   (budget: loops=50)
  step  1/10 done   (checkpointed cursor=1)
  …
  step  5/10 done   (checkpointed cursor=5)
⏸  did 5 steps, then stopping (the demo crashes the daemon here).

── 3. kill -9 the daemon  (a hard crash — no graceful shutdown) ───────
   daemon killed. restarting it…
   ✓ msg="resumed runs from store" count=1

── 4. re-run the agent: it RESUMES and finishes ───────────────────────
↻ RESUMING run 4e25ad71-…
  the governor already counts 5 spent steps — resuming at cursor 5, not redoing them.
  step  6/10 done   (checkpointed cursor=6)
  …
  step 10/10 done   (checkpointed cursor=10)
✅ completed all 10 steps. governor loop counter = 10 — exactly one per step of
   work. The steps finished before the crash were neither redone nor re-paid.

── 5. proof — the run did 10 steps total across the crash, not 15 ─────
  …  kill-9-resume  running  …  LOOPS=10  …
```

## Do it by hand (for a live recording)

```bash
# terminal 1 — the daemon
riskkernel serve

# terminal 2 — the agent. it checkpoints each step.
python agent.py
#   …step 1…2…3…  ← now CRASH the daemon: in terminal 1, Ctrl-C twice or `kill -9 <pid>`
#   the agent prints: "💥 the daemon is gone … restart and re-run to resume"

# terminal 1 — restart it; it logs  "resumed runs from store count=1"
riskkernel serve

# terminal 2 — re-run the SAME script. it resumes from the last checkpoint:
python agent.py
#   "↻ RESUMING … not redoing them"  → finishes → loop counter = number of steps, not double
```

## How it works

Three pieces, all already in the runtime — the example just wires them:

1. **Checkpoint each step.** `run.checkpoint("progress", {"cursor": i+1})` durably
   saves *where you are* in the run's SQLite state (the daemon also snapshots
   cumulative usage after every step).
2. **Reload on restart.** The daemon reloads non-terminal runs on boot,
   reconstructing each governor with the budget and usage it had already spent —
   so enforcement continues; a SIGKILL can't reset the meter.
3. **Attach and continue.** `with rt.resume_run(run_id) as run:` re-attaches to the
   run (it neither creates a new one nor cancels it); `run.latest_checkpoint()`
   gives the cursor to resume from. The remaining steps run against the **same
   budget**.

## The "$ not double-counting" headline

This demo counts **loops** so it needs no API key. The **dollar** counter behaves
identically: `Reload` restores spent dollars and tokens too, so a run that had
burned `$4.20` of a `$5` budget resumes at `$4.20`, not `$0` — it can't get a fresh
budget by crashing. To see it on real spend, route your model through the run's
proxy (`run.proxy_config()`, as in [`examples/codebase-qa`](../codebase-qa)) and
watch `riskkernel audit export <run-id>` before and after the crash.

## A note on the crash instant

If the daemon dies *in the middle of a step* (after the loop is counted but before
that step's checkpoint), the daemon rolls that partial step back on restart — so the
**budget is charged exactly once**, never twice. At most one step's *work* is
re-attempted, never the whole run; because that step can run twice, side-effecting
tools should be idempotent. The full model — what's restored, exact-once semantics,
and idempotency — is in the [crash-resume guide](../../docs/RESUME.md).

## Tuning for a recording

- `RK_TOTAL_STEPS` (default 10), `RK_WORK_SECONDS` (default 0.4) — total work and
  per-step delay. `RK_STOP_AFTER` makes the agent stop cleanly after N steps (the
  orchestrator uses it to crash at a known point).
