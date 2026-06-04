# mcp — govern an MCP server's tool calls

RiskKernel's **MCP gateway** is a JSON-RPC reverse proxy that sits in front of your
real MCP server. Point your MCP client at the gateway instead of the server, and
every `tools/call` is governed:

- **Allowlist** (deterministic) — only the tools you permit can be called; anything
  else is refused *before it leaves the gateway*.
- **Approval gate** (human-in-the-loop) — side-effecting tools pause for a human to
  approve or deny (CLI / local web / webhook).
- **Audit trail** (append-only) — every call, allowed or refused, is recorded on
  your disk.

Allowed, approved calls pass through untouched — the governance is invisible to them.

**No API key, no model call.** This example runs a tiny stand-in MCP server in the
background (in real life that's *your* server), then drives three calls through the
gateway. `demo.py` is **stdlib-only** — no `pip install`.

## Run it in 60 seconds

```bash
# 1. start the daemon governing MCP, pointed at this demo's stub server (:9001).
#    A binary is easiest here (localhost upstream); Docker note below.
RISKKERNEL_MCP_UPSTREAM=http://localhost:9001 \
RISKKERNEL_MCP_ALLOWLIST='search,write_file' \
RISKKERNEL_MCP_READONLY='search' \
riskkernel serve

# 2. in another terminal:
cd examples/mcp
python demo.py
```

`search` is allowed and read-only; `write_file` is allowed but side-effecting;
anything else (like `delete_everything`) is off the allowlist.

## What you'll see

```
▶ mcp   gateway=http://localhost:7070/mcp   upstream(stub)=:9001   run=mcp-demo

1) read-only, allowed → forwarded to the upstream:
  ✅ search            forwarded  → [upstream MCP server] executed search

2) not on the allowlist → blocked before it leaves the gateway:
  🛑 delete_everything refused    → tool not allowed by policy: delete_everything  (jsonrpc -32001)

3) side-effecting → held for human approval, then DENIED:
  🛑 write_file        refused    → approval denied for tool: write_file  (jsonrpc -32003)

— audit trail (what the gateway recorded, on your disk) —
  search            approved
  delete_everything blocked
  write_file        denied  [tool]

  full JSON:  riskkernel audit tools mcp-demo
```

- The blocked tool returns JSON-RPC error **-32001** and **never reaches the upstream**.
- The side-effecting tool blocks until a human resolves it; here the script stands
  in for the reviewer and denies it (**-32003**). In real use you'd approve/deny from
  `riskkernel approvals`, the local web page at `/admin/approvals`, or a webhook.

The full, signed-on-disk record:

```bash
riskkernel audit tools mcp-demo
```

## Point it at your real MCP server

There's nothing demo-specific in the gateway — drop the env vars on your own
server and your own MCP client:

```bash
RISKKERNEL_MCP_UPSTREAM=https://your-mcp-server.example/mcp \
RISKKERNEL_MCP_ALLOWLIST='read_*,search,create_issue' \
RISKKERNEL_MCP_READONLY='read_*,search' \
riskkernel serve
# then point your MCP client's server URL at http://localhost:7070/mcp
# and group calls into a run with the header  X-RiskKernel-Run-Id: <your-run-id>
```

- `RISKKERNEL_MCP_ALLOWLIST` — comma-separated tool names or globs (empty = allow all).
- `RISKKERNEL_MCP_READONLY` — tools that never need approval; everything else
  side-effecting is gated when the approval gate is in its default (fail-closed) mode.

**Docker:** run the daemon with `RISKKERNEL_MCP_UPSTREAM=http://host.docker.internal:9001`
so the container can reach a stub running on your host (`--add-host` may be needed
on Linux). With a real, routable MCP server URL, plain Docker is fine.

## Tuning

- `MCP_STUB_PORT` (default `9001`) — the port the stand-in server binds; match it in
  `RISKKERNEL_MCP_UPSTREAM`.

Nothing about the refusals is faked: the gateway returns the JSON-RPC error itself,
and the upstream stub is never contacted for a blocked or denied call.
