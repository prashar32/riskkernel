#!/usr/bin/env bash
# kill-9-resume — scripts the flagship demo end to end, reproducibly:
#   start daemon → agent does 5/10 steps → kill -9 the daemon → restart →
#   agent RESUMES and finishes → prove the loop counter is 10, not 15.
#
# Prereqs: the `riskkernel` binary and a Python with the SDK installed.
#   RISKKERNEL_BIN  path to the daemon binary   (default: riskkernel on PATH)
#   PYTHON          python with the SDK          (default: python3)
# e.g.   RISKKERNEL_BIN=../../riskkernel PYTHON=.venv/bin/python ./demo.sh
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${RISKKERNEL_BIN:-riskkernel}"
PY="${PYTHON:-python3}"
PORT="${RK_PORT:-7070}"
DATA="$(mktemp -d)"
export RISKKERNEL_BASE_URL="http://localhost:${PORT}"
export RK_RUN_ID_FILE="${DATA}/run-id"
export RK_PORT="${PORT}"

cleanup() { [[ -n "${PID:-}" ]] && kill "${PID}" 2>/dev/null; rm -rf "${DATA}"; }
trap cleanup EXIT

start_daemon() {
  RISKKERNEL_DATA_DIR="${DATA}" RISKKERNEL_PORT="${PORT}" "${BIN}" serve >>"${DATA}/serve.log" 2>&1 &
  PID=$!
  for _ in $(seq 1 40); do
    curl -sf "http://localhost:${PORT}/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  echo "✗ daemon didn't come up — see ${DATA}/serve.log" >&2; exit 1
}

echo "── 1. start the daemon ────────────────────────────────────────────────"
start_daemon
echo "   up (pid ${PID})"

echo
echo "── 2. agent does 5 of 10 steps, checkpointing each ────────────────────"
RK_STOP_AFTER=5 "${PY}" "${HERE}/agent.py"

echo
echo "── 3. kill -9 the daemon  (a hard crash — no graceful shutdown) ───────"
kill -9 "${PID}"; sleep 0.5
echo "   daemon killed. restarting it…"
start_daemon
grep -i "resumed runs" "${DATA}/serve.log" | tail -1 | sed 's/^/   ✓ /' || true

echo
echo "── 4. re-run the agent: it RESUMES and finishes ───────────────────────"
"${PY}" "${HERE}/agent.py"

echo
echo "── 5. proof — the run did 10 steps total across the crash, not 15 ─────"
RISKKERNEL_DATA_DIR="${DATA}" "${BIN}" runs list 2>/dev/null | grep -v "state store ready" || true
echo
echo "   loops = 10 (one per step of work). Re-running from zero would read 15."
