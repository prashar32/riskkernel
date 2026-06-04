#!/usr/bin/env python3
"""mcp — govern an MCP server's tool calls with RiskKernel.

RiskKernel's MCP gateway is a JSON-RPC reverse proxy in front of your real MCP
server. Point your MCP client at the gateway instead of the server and every
``tools/call`` is governed — a per-tool allowlist (deterministic), an approval
gate for side-effecting tools (human-in-the-loop), and an append-only audit
trail — while allowed, approved calls pass through untouched.

This script runs a tiny stand-in MCP server in the background (in real life that's
*your* server), then sends three calls through the gateway:

  1. search            — allowed + read-only      → forwarded to the upstream
  2. delete_everything — NOT on the allowlist      → blocked before it leaves the gateway
  3. write_file        — allowed but side-effecting → held for approval, then DENIED

…and prints the audit trail the gateway recorded. No API key, no model call.

Run it:
    # 1. start the daemon governing MCP, pointed at this demo's stub server (:9001):
    RISKKERNEL_MCP_UPSTREAM=http://localhost:9001 \
    RISKKERNEL_MCP_ALLOWLIST='search,write_file' \
    RISKKERNEL_MCP_READONLY='search' \
    riskkernel serve

    # 2. in another terminal:
    python demo.py
"""

from __future__ import annotations

import json
import os
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

GATEWAY = os.environ.get("RISKKERNEL_BASE_URL", "http://localhost:7070")
STUB_PORT = int(os.environ.get("MCP_STUB_PORT", "9001"))
RUN = "mcp-demo"


# ── a stand-in upstream MCP server (in real life, this is YOUR MCP server) ───────
class _StubMCP(BaseHTTPRequestHandler):
    def log_message(self, *a):  # quiet
        pass

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0) or 0)
        try:
            req = json.loads(self.rfile.read(n) or b"{}")
        except Exception:
            req = {}
        tool = (req.get("params") or {}).get("name", "?")
        body = json.dumps({
            "jsonrpc": "2.0", "id": req.get("id"),
            "result": {"content": [{"type": "text", "text": f"[upstream MCP server] executed {tool}"}]},
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def start_stub() -> None:
    srv = ThreadingHTTPServer(("127.0.0.1", STUB_PORT), _StubMCP)
    threading.Thread(target=srv.serve_forever, daemon=True).start()


# ── tiny stdlib HTTP helpers ─────────────────────────────────────────────────────
def _http(method: str, path: str, body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    h = {"Content-Type": "application/json"}
    h.update(headers or {})
    req = urllib.request.Request(GATEWAY + path, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, (json.loads(raw) if raw else {})
    except urllib.error.URLError as e:
        raise SystemExit(
            f"\n✗ can't reach the daemon at {GATEWAY}: {e.reason}\n"
            "  start it with the MCP env vars first — see the README."
        )


def mcp_call(tool: str, arguments: dict):
    """Send one tools/call through the RiskKernel MCP gateway."""
    return _http("POST", "/mcp", {
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {"name": tool, "arguments": arguments},
    }, {"X-RiskKernel-Run-Id": RUN})


def show(tool: str, code: int, body) -> None:
    if code == 404:
        raise SystemExit(
            "\n✗ the MCP gateway isn't enabled on this daemon (POST /mcp → 404).\n"
            "  start it with RISKKERNEL_MCP_UPSTREAM set — see the README."
        )
    if isinstance(body, dict) and "result" in body:
        text = body["result"]["content"][0]["text"]
        print(f"  ✅ {tool:<17} forwarded  → {text}")
    elif isinstance(body, dict) and "error" in body:
        e = body["error"]
        print(f"  \U0001f6d1 {tool:<17} refused    → {e['message']}  (jsonrpc {e['code']})")
    else:
        print(f"  ?  {tool:<17} code={code} body={body}")


def deny_when_pending() -> bool:
    """Stand in for a human reviewer: wait until the side-effecting call is pending,
    then deny it through the approval API."""
    for _ in range(400):
        _, run = _http("GET", f"/v1/runs/{RUN}")
        if run.get("status") == "waiting_approval":
            _http("POST", f"/v1/runs/{RUN}/approve", {
                "decision": "deny",
                "reason": "writing to disk isn't allowed in this demo",
                "decidedBy": "demo-operator",
            })
            return True
        time.sleep(0.05)
    return False


def main() -> None:
    start_stub()
    print(f"▶ mcp   gateway={GATEWAY}/mcp   upstream(stub)=:{STUB_PORT}   run={RUN}\n")

    print("1) read-only, allowed → forwarded to the upstream:")
    show("search", *mcp_call("search", {"q": "invoices"}))

    print("\n2) not on the allowlist → blocked before it leaves the gateway:")
    show("delete_everything", *mcp_call("delete_everything", {"path": "/"}))

    print("\n3) side-effecting → held for human approval, then DENIED:")
    out: dict = {}
    t = threading.Thread(
        target=lambda: out.update(r=mcp_call("write_file", {"path": "/etc/hosts"})),
        daemon=True,
    )
    t.start()
    if not deny_when_pending():
        print("   (no approval became pending — is the daemon's approval gate disabled?)")
    t.join(timeout=15)
    if "r" in out:
        show("write_file", *out["r"])

    print("\n— audit trail (what the gateway recorded, on your disk) —")
    _, calls = _http("GET", f"/v1/runs/{RUN}/tool-calls")
    for c in calls if isinstance(calls, list) else []:
        se = f"  [{c['sideEffect']}]" if c.get("sideEffect") else ""
        print(f"  {c['tool']:<17} {c['status']}{se}")
    print(f"\n  full JSON:  riskkernel audit tools {RUN}")


if __name__ == "__main__":
    main()
