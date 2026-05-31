#!/usr/bin/env python3
"""codebase-qa — a tiny, real codebase Q&A agent governed by RiskKernel.

It's a plain ReAct-style loop (no RAG, no vector DB, no framework): each step it
asks the model what to do, READs a file or ANSWERs, and repeats. Every model call
goes through the RiskKernel proxy (via the SDK's ``run.proxy_config()``), so the
deterministic governor meters cost and enforces the per-run loop / dollar / time
budget around the loop.

Two modes (``--mode``):
  normal   — a sensible question that completes within budget; prints each step,
             tokens, and running USD cost, then the answer.
  runaway  — the SAME agent with a deliberately weak stopping condition (it's told
             to re-read every file before answering), so it loops. The counters
             climb each step and then the governor's LOOP budget halts the run
             cleanly — the kill comes from RiskKernel, not from this script.

BYO key: you run `riskkernel serve` with your ANTHROPIC_API_KEY; this agent only
talks to the daemon. Nothing else is required.

This file doubles as RiskKernel Python SDK documentation — the governance bits are
``rt.budget(...)``, ``rt.governed_run(...)``, ``run.proxy_config()``,
``run.status()``, and catching ``rk.BudgetExceeded``.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

import riskkernel as rk

# ─────────────────────────────────────────────────────────────────────────────
# Tweakable constants — tune these for a clean recording. Everything here is real;
# nothing about the kill is faked. The governor (in the daemon) does the enforcing.
# ─────────────────────────────────────────────────────────────────────────────
DAEMON_URL = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
API_TOKEN = os.environ.get("RISKKERNEL_API_TOKEN")  # only if your daemon sets one
MODEL = os.environ.get("RK_DEMO_MODEL", "claude-haiku-4-5-20251001")  # cheap + fast
MAX_OUTPUT_TOKENS = 200          # small responses keep the demo cheap
MAX_FILE_CHARS = 1_500           # cap each file read so token use stays modest

# Budgets are per-run hard limits enforced by the governor.
NORMAL_BUDGET = dict(loops=10, dollars=0.10, seconds=120)   # generous: finishes
RUNAWAY_BUDGET = dict(loops=4, dollars=0.05, seconds=120)   # small loop cap: kills fast

# Belt-and-suspenders: the governor should ALWAYS fire before this Python-side cap.
# It exists only so a misconfigured (too-generous) budget can't loop forever.
SAFETY_ITERS = 50


def build_system_prompt(files: list[str], mode: str) -> str:
    listing = "\n".join(f"  - {f}" for f in files)
    base = (
        "You are a codebase Q&A agent. You can use exactly two tools, one per turn.\n"
        "Reply with a SINGLE line, nothing else:\n"
        "  READ <relative/path>   — read a file before answering\n"
        "  ANSWER <your answer>   — give the final answer and stop\n\n"
        f"Files available to READ:\n{listing}\n"
    )
    if mode == "runaway":
        # A deliberately weak stopping condition — a real failure mode. The agent
        # is told to be paranoid and re-verify, so it keeps READing and never
        # converges. This is honest: the agent has a bad heuristic; RiskKernel is
        # what stops it.
        base += (
            "\nBE EXTREMELY THOROUGH. Do NOT ANSWER until you have re-read EVERY file "
            "at least twice to cross-check yourself. If you are not 100% certain you "
            "have re-read everything, READ the next file again instead of answering."
        )
    else:
        base += "\nRead only the files you need, then ANSWER concisely."
    return base


def list_files(directory: Path) -> list[str]:
    exts = {".py", ".md", ".txt", ".yaml", ".yml", ".go", ".js", ".ts"}
    out = []
    for p in sorted(directory.rglob("*")):
        if p.is_file() and p.suffix.lower() in exts:
            out.append(str(p.relative_to(directory)))
        if len(out) >= 12:
            break
    return out


def read_file(directory: Path, name: str) -> str:
    # Stay within the target directory (no traversal); the daemon's memory layer
    # has the same guard — here we just keep the demo honest.
    target = (directory / name).resolve()
    if not str(target).startswith(str(directory.resolve())):
        return "(refused: path escapes the target directory)"
    if not target.is_file():
        return f"(no such file: {name})"
    return target.read_text(errors="replace")[:MAX_FILE_CHARS]


def call_llm(cfg: dict, system: str, messages: list[dict]) -> str:
    """One model call THROUGH the RiskKernel proxy. The proxy meters tokens/cost
    and enforces the run's budget; a spent budget comes back as HTTP 402, which we
    surface as rk.BudgetExceeded so the loop halts on the governor's verdict."""
    body = json.dumps({
        "model": MODEL,
        "max_tokens": MAX_OUTPUT_TOKENS,
        "messages": [{"role": "system", "content": system}] + messages,
    }).encode()
    headers = {"content-type": "application/json", **cfg["headers"]}
    if API_TOKEN:
        headers["Authorization"] = "Bearer " + API_TOKEN
    req = urllib.request.Request(cfg["base_url"] + "/chat/completions", data=body, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            data = json.loads(resp.read())
        return data["choices"][0]["message"]["content"].strip()
    except urllib.error.HTTPError as e:
        payload = {}
        try:
            payload = json.loads(e.read())
        except Exception:
            pass
        if e.code == 402:  # the governor halted the run
            raise rk.BudgetExceeded(payload.get("code", "budget_exceeded"),
                                    payload.get("message", "")) from None
        raise rk.APIError(e.code, payload.get("code", ""), payload.get("message", str(e))) from None
    except urllib.error.URLError as e:  # daemon unreachable
        raise rk.APIError(0, "connection_error", str(e.reason)) from None


def parse_action(reply: str):
    line = reply.strip().splitlines()[0].strip() if reply.strip() else ""
    if line.upper().startswith("READ "):
        return "READ", line[5:].strip()
    if line.upper().startswith("ANSWER"):
        return "ANSWER", line[6:].lstrip(": ").strip() or reply
    return "ANSWER", reply  # fallback: treat anything else as the answer


def run_agent(directory: Path, question: str, mode: str) -> int:
    files = list_files(directory)
    if not files:
        print(f"no readable files under {directory}", file=sys.stderr)
        return 2

    rt = rk.Runtime(base_url=DAEMON_URL, token=API_TOKEN)
    budget = rt.budget(**(RUNAWAY_BUDGET if mode == "runaway" else NORMAL_BUDGET))
    system = build_system_prompt(files, mode)
    messages = [{"role": "user", "content": f"Question: {question}"}]

    print(f"▶ codebase-qa  mode={mode}  dir={directory}  model={MODEL}")
    print(f"  budget: loops={budget.loops} dollars=${budget.dollars} seconds={budget.seconds}")
    print(f"  question: {question}\n")

    # cancel_on_error=False so a budget halt leaves the run exactly as the governor
    # left it (status 'halted', the real halt reason) rather than 'cancelled'.
    try:
        with rt.governed_run(name=f"codebase-qa-{mode}", budget=budget,
                             cancel_on_error=False) as run:
            cfg = run.proxy_config()
            print(f"  run: {run.id}\n")
            for _ in range(SAFETY_ITERS):
                reply = call_llm(cfg, system, messages)        # ← metered + governed by the proxy
                u = run.status()["usage"]                       # ← live ledger from the daemon
                action, arg = parse_action(reply)
                summary = f"READ {arg}" if action == "READ" else "ANSWER"
                print(f"  step {u['loops']:>2} │ {summary:<28} │ tokens={u['tokens']:>5} │ cost=${u['dollars']:.4f}")

                messages.append({"role": "assistant", "content": reply})
                if action == "ANSWER":
                    print(f"\n✅ completed within budget.\n\n— Answer —\n{arg}\n")
                    return 0
                # READ: feed the file back and keep going.
                content = read_file(directory, arg)
                run.checkpoint("after-read", {"file": arg})     # ← resumable state
                messages.append({"role": "user", "content": f"Contents of {arg}:\n{content}"})

            print("\n(safety cap reached — your budget is too generous to demo the kill; lower RUNAWAY_BUDGET)")
            return 0
    except rk.BudgetExceeded as e:
        # The kill came from the governor in the daemon — not from this script.
        final = rt.client.get_run(run.id)
        u = final["usage"]
        print(f"\n🛑 RiskKernel HALTED the run — reason: {e.reason}")
        print("   ── final ledger ─────────────────────────────")
        print(f"     steps (loops) : {u['loops']}")
        print(f"     tokens        : {u['tokens']}")
        print(f"     cost          : ${u['dollars']:.4f}")
        print(f"     run status    : {final['status']}  (state persisted & resumable)")
        print(f"     run id        : {run.id}")
        print("   The agent would have looped forever; the governor stopped it cleanly.\n")
        return 0
    except rk.APIError as e:
        if "connection" in (e.code or "") or e.status == 0:
            print(f"\nCannot reach the RiskKernel daemon at {DAEMON_URL}.", file=sys.stderr)
            print("Start it first:  riskkernel serve   (with ANTHROPIC_API_KEY set)\n", file=sys.stderr)
        else:
            print(f"\nAPI error: {e}", file=sys.stderr)
        return 1


def main() -> int:
    ap = argparse.ArgumentParser(description="RiskKernel-governed codebase Q&A agent")
    ap.add_argument("--mode", choices=["normal", "runaway"], required=True,
                    help="normal = completes within budget; runaway = loops until the governor kills it")
    ap.add_argument("--dir", default=str(Path(__file__).parent / "sample"),
                    help="target codebase directory (default: bundled ./sample)")
    ap.add_argument("--question", default="What does this codebase do and where is the entrypoint?",
                    help="the question to answer")
    args = ap.parse_args()
    return run_agent(Path(args.dir), args.question, args.mode)


if __name__ == "__main__":
    raise SystemExit(main())
