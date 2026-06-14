#!/usr/bin/env python3
"""RiskKernel recovery benchmark — timed crash-resume with exact-once spend.

The companion to benchmark.py (the cost dimension). This measures the *recovery*
dimension: a governed, checkpointing run is interrupted by a hard `kill -9` of the
daemon mid-run, the daemon is restarted on the same durable data dir, and we prove
the run finishes WITHOUT re-spending — the cost counter continues from where it was
(no reset, no double-count) and the run still halts at its original budget. That's
exact-once spend across a crash.

What it measures:

  1. A checkpointing run makes several governed calls through RiskKernel (run-id
     header groups them; a checkpoint is saved between steps). Spend is read from
     RiskKernel's own ledger (GET /v1/runs/{id} -> usage.dollars / tokens / loops).
  2. The daemon is SIGKILL'd (no graceful shutdown) mid-run.
  3. The daemon is restarted on the SAME data dir; we time how long until it is
     healthy AND the run has been reloaded with the spend it already had
     (RECOVERY TIME).
  4. The run continues calling until the dollar budget halts; we confirm the meter
     resumed from the pre-crash spend (not 0, not doubled) and the total halted
     spend equals exactly the budget.

Everything is deterministic and key-free: the same mock_provider.py the cost
benchmark uses, reached via RISKKERNEL_OPENAI_BASE_URL with a dummy OPENAI_API_KEY,
models routed to openai ("gpt-4o"). No network, no real money.

Run:  python3 benchmark/recovery.py
Env:  RK_BIN (default "riskkernel"), N, BUDGET, PRE_CRASH_CALLS, PORT, MOCK_PORT
"""
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
RK_BIN = os.environ.get("RK_BIN", "riskkernel")
N = int(os.environ.get("N", "50"))                       # max calls (safety cap on the loop)
BUDGET = float(os.environ.get("BUDGET", "0.25"))         # dollar ceiling for the run
PRE_CRASH_CALLS = int(os.environ.get("PRE_CRASH_CALLS", "8"))  # calls to make before the kill -9
PORT = int(os.environ.get("PORT", "7070"))
MOCK_PORT = int(os.environ.get("MOCK_PORT", "9099"))
MODEL = "gpt-4o"
RUN_ID = "bench-recovery"
RK_URL = f"http://127.0.0.1:{PORT}"
MOCK_URL = f"http://127.0.0.1:{MOCK_PORT}"
RUN_URL = f"{RK_URL}/v1/runs/{RUN_ID}"
CHAT_URL = f"{RK_URL}/v1/chat/completions"
RUN_HEADERS = {"X-RiskKernel-Run-Id": RUN_ID}
CHAT_BODY = json.dumps({"model": MODEL, "messages": [{"role": "user", "content": "continue"}]}).encode()


def post(url, body=CHAT_BODY, headers=None, timeout=15):
    req = urllib.request.Request(url, data=body, method="POST",
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, b""
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception:
        return 0, b""  # connection refused while a server is still starting/down


def get_json(url, timeout=5):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return json.loads(r.read())


def wait_get(url, timeout=25):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1)
            return True
        except Exception:
            time.sleep(0.05)
    return False


def wait_post(url, timeout=10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if post(url, timeout=1)[0] == 200:
            return True
        time.sleep(0.1)
    return False


def usage_of(run):
    u = run.get("usage", {})
    return {
        "dollars": float(u.get("dollars", 0.0)),
        "tokens": int(u.get("tokens", 0)),
        "loops": int(u.get("loops", 0)),
    }


def serve_env(data_dir):
    return dict(os.environ,
                RISKKERNEL_PORT=str(PORT),
                RISKKERNEL_DATA_DIR=data_dir,
                RISKKERNEL_DEFAULT_PROVIDER="openai",
                OPENAI_API_KEY="bench-dummy-key",
                RISKKERNEL_OPENAI_BASE_URL=MOCK_URL,
                RISKKERNEL_DEFAULT_DOLLARS=str(BUDGET),
                RISKKERNEL_DEFAULT_LOOPS="0",   # only the dollar budget halts the loop
                RISKKERNEL_DEFAULT_SECONDS="0",
                RISKKERNEL_PRICING_FILE=os.path.join(HERE, "pricing.json"))


def start_daemon(data_dir, log_path):
    """Start `riskkernel serve` on data_dir, logging to log_path. Returns the Popen."""
    log = open(log_path, "ab")
    return subprocess.Popen([RK_BIN, "serve"], env=serve_env(data_dir),
                            stdout=log, stderr=log)


def make_calls(n):
    """Make up to n governed calls on RUN_ID; stop early on the first non-200 (a
    budget halt). Returns (calls_made, halt_reason)."""
    calls, halt = 0, ""
    for _ in range(n):
        code, body = post(CHAT_URL, headers=RUN_HEADERS)
        if code != 200:
            try:
                halt = json.loads(body).get("code") or f"http {code}"
            except Exception:
                halt = f"http {code}"
            break
        calls += 1
    return calls, halt


def checkpoint(cursor):
    body = json.dumps({"name": "progress", "payload": {"cursor": cursor}}).encode()
    post(f"{RUN_URL}/checkpoints", body=body, headers=None)


def wait_recovered(deadline_s=25):
    """Time from now until the daemon is healthy AND the crashed run has been
    reloaded with its prior spend. Returns (recovery_seconds, reloaded_run)."""
    start = time.time()
    if not wait_get(f"{RK_URL}/healthz", timeout=deadline_s):
        sys.exit("riskkernel did not come back up after the crash")
    # Healthy isn't enough: confirm the run itself is reloaded and still 'running'
    # with the usage it had before the crash (Reload restores non-terminal runs).
    deadline = time.time() + deadline_s
    while time.time() < deadline:
        try:
            run = get_json(RUN_URL)
            if run.get("status") == "running" and usage_of(run)["loops"] > 0:
                return time.time() - start, run
        except Exception:
            pass
        time.sleep(0.02)
    sys.exit("the crashed run was not reloaded after restart")


def grep_resumed(log_path):
    """Return the 'resumed runs from store' startup line, if present (proof the
    daemon restored runs from the durable store on restart)."""
    try:
        with open(log_path, "r", errors="ignore") as f:
            for line in reversed(f.readlines()):
                if "resumed runs from store" in line:
                    return line.strip()
    except Exception:
        pass
    return ""


def main():
    data_dir = os.path.join(HERE, ".recovery-data")
    log_path = os.path.join(HERE, ".recovery-serve.log")
    shutil.rmtree(data_dir, ignore_errors=True)
    if os.path.exists(log_path):
        os.remove(log_path)

    mock = subprocess.Popen([sys.executable, os.path.join(HERE, "mock_provider.py"), str(MOCK_PORT)])
    rk = start_daemon(data_dir, log_path)
    rk2 = None
    try:
        if not wait_post(f"{MOCK_URL}/v1/chat/completions"):
            sys.exit("mock provider did not come up")
        if not wait_get(f"{RK_URL}/healthz"):
            sys.exit("riskkernel did not come up")

        # 1. A checkpointing run makes several governed calls (under the budget).
        before_calls = 0
        for i in range(min(PRE_CRASH_CALLS, N)):
            code, _ = post(CHAT_URL, headers=RUN_HEADERS)
            if code != 200:
                break
            before_calls += 1
            checkpoint(before_calls)  # save WHERE we are between steps, durably

        before = usage_of(get_json(RUN_URL))
        if before["loops"] == 0:
            sys.exit("no spend recorded before the crash — cannot test resume")

        # 2. kill -9 the daemon mid-run — a hard crash, no graceful shutdown.
        rk.send_signal(signal.SIGKILL)
        rk.wait(5)

        # 3. Restart on the SAME data dir; time recovery (healthy + run reloaded).
        rk2 = start_daemon(data_dir, log_path)
        recovery_s, reloaded = wait_recovered()
        after_restart = usage_of(reloaded)
        resumed_line = grep_resumed(log_path)

        # 4. Continue the run to completion; it must halt at the ORIGINAL budget.
        remaining_calls = N - before_calls
        post_calls, halt = make_calls(remaining_calls)
        final = usage_of(get_json(RUN_URL))

        # --- exact-once checks ---
        per_call = before["dollars"] / before["loops"] if before["loops"] else 0.0
        no_reset = after_restart["dollars"] >= before["dollars"] - 1e-9
        no_double = abs(after_restart["dollars"] - before["dollars"]) < 1e-9
        # The run's final loop count must equal the budget ceiling — one charge per
        # call, never re-charging the pre-crash calls. If the meter had reset, it
        # would take ~before_calls MORE calls (and total loops > ceiling) to halt.
        ceiling_loops = int(round(BUDGET / per_call)) if per_call else 0
        exact_once = (
            no_double
            and final["loops"] == ceiling_loops
            and abs(final["dollars"] - BUDGET) < per_call / 2
            and halt == "dollar_budget_exceeded"
        )

        # Counterfactual: if the crash had RESET the meter to $0, the post-crash
        # phase would have run a FULL budget's worth of calls again (ceiling_loops)
        # before halting — so the run would have spent the pre-crash calls plus a
        # whole second ceiling. That's the double-spend exact-once prevents.
        reset_would_have_spent = (before_calls + ceiling_loops) * per_call

        print("\n  RiskKernel recovery benchmark — kill -9 mid-run, resume without re-spending")
        print("  " + "-" * 66)
        print(f"  dollar budget              ${BUDGET:.2f}")
        print(f"  per-call cost              ${per_call:.4f}   ({MODEL}, from RiskKernel's ledger)")
        print(f"  calls before crash         {before_calls}")
        print("  " + "-" * 66)
        print(f"  RECOVERY TIME              {recovery_s * 1000:.0f} ms   (kill -9 -> healthy + run reloaded)")
        if resumed_line:
            msg = resumed_line.split("msg=", 1)[-1] if "msg=" in resumed_line else resumed_line
            print(f"  daemon log                 {msg}")
        print("  " + "-" * 66)
        print(f"  {'spend (ledger)':28}{'dollars':>10}{'tokens':>9}{'loops':>7}")
        print(f"  {'before crash':28}{'$'+format(before['dollars'], '.4f'):>10}{before['tokens']:>9}{before['loops']:>7}")
        print(f"  {'after restart (reloaded)':28}{'$'+format(after_restart['dollars'], '.4f'):>10}{after_restart['tokens']:>9}{after_restart['loops']:>7}")
        print(f"  {'final (budget halt)':28}{'$'+format(final['dollars'], '.4f'):>10}{final['tokens']:>9}{final['loops']:>7}")
        print("  " + "-" * 66)
        print(f"  meter NOT reset by crash   {'yes' if no_reset else 'NO'}  (after-restart spend >= before)")
        print(f"  meter NOT double-counted   {'yes' if no_double else 'NO'}  (after-restart spend == before)")
        print(f"  halted at original budget  {'yes' if exact_once else 'NO'}  ({halt or 'did not halt'})")
        print(f"  EXACT-ONCE across crash    {'PASS' if exact_once else 'FAIL'}")
        print(f"  (a crash that reset the meter would have spent ${reset_would_have_spent:.4f} "
              f"— the ${final['dollars']:.4f} ceiling plus the {before_calls} pre-crash calls re-paid)")
        print()

        # Append a structured record alongside the cost benchmark's results.json,
        # without clobbering it: results.json stays the cost object; recovery lives
        # under a "recovery" key (back-compatible — readers of the old shape still work).
        results_path = os.path.join(HERE, "results.json")
        results = {}
        if os.path.exists(results_path):
            try:
                with open(results_path) as f:
                    results = json.load(f)
            except Exception:
                results = {}
        results["recovery"] = {
            "budget_dollars": BUDGET,
            "per_call_dollars": round(per_call, 6),
            "calls_before_crash": before_calls,
            "recovery_ms": round(recovery_s * 1000, 1),
            "before_crash": {k: round(v, 6) if k == "dollars" else v for k, v in before.items()},
            "after_restart": {k: round(v, 6) if k == "dollars" else v for k, v in after_restart.items()},
            "final": {k: round(v, 6) if k == "dollars" else v for k, v in final.items()},
            "halt_reason": halt,
            "meter_not_reset": no_reset,
            "meter_not_doubled": no_double,
            "exact_once": exact_once,
            "reset_would_have_spent_dollars": round(reset_would_have_spent, 6),
        }
        with open(results_path, "w") as f:
            json.dump(results, f, indent=2)
            f.write("\n")

        if not exact_once:
            sys.exit("EXACT-ONCE check FAILED — see the table above")
    finally:
        for p in (rk2, rk, mock):
            if p is None:
                continue
            p.terminate()
            try:
                p.wait(5)
            except Exception:
                p.kill()
        shutil.rmtree(data_dir, ignore_errors=True)
        if os.path.exists(log_path):
            os.remove(log_path)


if __name__ == "__main__":
    main()
