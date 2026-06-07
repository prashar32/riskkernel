#!/usr/bin/env python3
"""RiskKernel cost benchmark — reproducible "dollars saved" on a runaway loop.

The SAME looping agent runs twice against a deterministic mock provider:

  1. Baseline  — calls the provider directly, no governance. A stuck loop makes
     all N calls and spends the full amount.
  2. Governed  — calls through RiskKernel with a hard dollar budget. RiskKernel
     meters cost per call and halts the run at the ceiling.

The mock returns fixed token usage, so cost is exact and reproducible. The
governed run's spend is read from RiskKernel's own ledger (GET /v1/runs/{id});
the baseline spend is that same per-call price across the full loop. No API key,
no real money.

Run:  python3 benchmark/benchmark.py
Env:  RK_BIN (default "riskkernel"), N, BUDGET, PORT, MOCK_PORT
"""
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
RK_BIN = os.environ.get("RK_BIN", "riskkernel")
N = int(os.environ.get("N", "50"))                # runaway loop length
BUDGET = float(os.environ.get("BUDGET", "0.25"))  # dollar ceiling for the governed run
PORT = int(os.environ.get("PORT", "7070"))
MOCK_PORT = int(os.environ.get("MOCK_PORT", "9099"))
MODEL = "gpt-4o"
RK_URL = f"http://127.0.0.1:{PORT}"
MOCK_URL = f"http://127.0.0.1:{MOCK_PORT}"
CHAT_BODY = json.dumps({"model": MODEL, "messages": [{"role": "user", "content": "continue"}]}).encode()


def post(url, headers=None, timeout=15):
    req = urllib.request.Request(url, data=CHAT_BODY, method="POST",
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, b""
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception:
        return 0, b""  # connection refused while a server is still starting


def wait_get(url, timeout=25):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1)
            return True
        except Exception:
            time.sleep(0.2)
    return False


def wait_post(url, timeout=10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if post(url, timeout=1)[0] == 200:
            return True
        time.sleep(0.2)
    return False


def run_loop(url, headers):
    """Make up to N calls; stop early on the first non-200 (a budget halt)."""
    start = time.time()
    calls, halt = 0, ""
    for _ in range(N):
        code, body = post(url, headers=headers)
        if code != 200:
            try:
                halt = json.loads(body).get("code") or f"http {code}"
            except Exception:
                halt = f"http {code}"
            break
        calls += 1
    return calls, halt, time.time() - start


def main():
    data_dir = os.path.join(HERE, ".bench-data")
    shutil.rmtree(data_dir, ignore_errors=True)
    mock = subprocess.Popen([sys.executable, os.path.join(HERE, "mock_provider.py"), str(MOCK_PORT)])
    env = dict(os.environ,
               RISKKERNEL_PORT=str(PORT),
               RISKKERNEL_DATA_DIR=data_dir,
               RISKKERNEL_DEFAULT_PROVIDER="openai",
               OPENAI_API_KEY="bench-dummy-key",
               RISKKERNEL_OPENAI_BASE_URL=MOCK_URL,
               RISKKERNEL_DEFAULT_DOLLARS=str(BUDGET),
               RISKKERNEL_DEFAULT_LOOPS="0",    # only the dollar budget should stop the loop
               RISKKERNEL_DEFAULT_SECONDS="0",
               RISKKERNEL_PRICING_FILE=os.path.join(HERE, "pricing.json"))
    rk = subprocess.Popen([RK_BIN, "serve"], env=env)
    try:
        if not wait_post(f"{MOCK_URL}/v1/chat/completions"):
            sys.exit("mock provider did not come up")
        if not wait_get(f"{RK_URL}/healthz"):
            sys.exit("riskkernel did not come up")

        gov_calls, halt, gov_time = run_loop(f"{RK_URL}/v1/chat/completions",
                                             {"X-RiskKernel-Run-Id": "bench-governed"})
        run = json.loads(urllib.request.urlopen(f"{RK_URL}/v1/runs/bench-governed", timeout=5).read())
        gov_dollars = float(run.get("usage", {}).get("dollars", 0.0))

        base_calls, _, base_time = run_loop(f"{MOCK_URL}/v1/chat/completions", None)

        per_call = gov_dollars / gov_calls if gov_calls else 0.0
        base_dollars = base_calls * per_call
        saved = base_dollars - gov_dollars
        pct = (saved / base_dollars * 100) if base_dollars else 0.0

        print("\n  RiskKernel cost benchmark — runaway loop")
        print("  " + "-" * 54)
        print(f"  loop length (N)            {N}")
        print(f"  dollar budget              ${BUDGET:.2f}")
        print(f"  per-call cost              ${per_call:.4f}   ({MODEL}, from RiskKernel's ledger)")
        print("  " + "-" * 54)
        print(f"  {'':24}{'calls':>7}{'spend':>13}")
        print(f"  {'baseline (no governance)':24}{base_calls:>7}{'$'+format(base_dollars, '.4f'):>13}")
        print(f"  {'governed (RiskKernel)':24}{gov_calls:>7}{'$'+format(gov_dollars, '.4f'):>13}")
        print("  " + "-" * 54)
        print(f"  dollars saved              ${saved:.4f}   ({pct:.0f}%)")
        print(f"  stopped by                 {halt}")
        print(f"  wall time base / governed  {base_time:.2f}s / {gov_time:.2f}s")
        print()

        with open(os.path.join(HERE, "results.json"), "w") as f:
            json.dump({
                "loop_length": N, "budget_dollars": BUDGET, "per_call_dollars": round(per_call, 6),
                "baseline_calls": base_calls, "baseline_dollars": round(base_dollars, 6),
                "governed_calls": gov_calls, "governed_dollars": round(gov_dollars, 6),
                "dollars_saved": round(saved, 6), "percent_saved": round(pct, 1), "halt_reason": halt,
            }, f, indent=2)
            f.write("\n")
    finally:
        for p in (rk, mock):
            p.terminate()
            try:
                p.wait(5)
            except Exception:
                p.kill()
        shutil.rmtree(data_dir, ignore_errors=True)


if __name__ == "__main__":
    main()
