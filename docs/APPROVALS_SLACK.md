# Slack approvals

Route RiskKernel's human-in-the-loop gate to Slack: when a governed, side-effecting
tool call needs sign-off, RiskKernel posts it to a channel with **Approve / Deny**
buttons, and the click resolves the pending action — no terminal, no dashboard.

It's a push channel alongside the CLI, the local web page, and the webhook; the same
deterministic policy decides *what* needs approval (the LLM never does). Approving
from Slack is identical to `POST /v1/runs/{id}/approve` — Slack is just the transport.

## Set it up (one-time)

1. **Create a Slack app** (https://api.slack.com/apps → *From scratch*), add it to
   your workspace, and invite its bot user to the channel you want approvals in.
2. **Bot token** — under *OAuth & Permissions*, add the `chat:write` bot scope,
   install the app, and copy the **Bot User OAuth Token** (`xoxb-…`).
3. **Interactivity** — under *Interactivity & Shortcuts*, turn it on and set the
   **Request URL** to your daemon's public endpoint:
   `https://<your-riskkernel-host>/v1/integrations/slack/interactions`
4. **Signing secret** — copy it from *Basic Information → App Credentials*. RiskKernel
   verifies every interaction against this, so a forged button click is rejected.

Then point the daemon at it:

```bash
export RISKKERNEL_APPROVAL_SLACK_BOT_TOKEN=xoxb-…          # chat:write
export RISKKERNEL_APPROVAL_SLACK_CHANNEL=C0123456789       # channel id
export RISKKERNEL_APPROVAL_SLACK_SIGNING_SECRET=…          # verifies button clicks
```

On startup you'll see `slack approval channel enabled`. The channel id is the `C…`
from *channel → View details*, not the `#name`.

## What you'll see

A gated call posts a message like:

> **Approval required**
> **Tool:** `mcp://shell`   **Side effect:** `exec`
> **Run:** `4f3c…`   **Step:** 7
> **Arguments:** `{ "cmd": "rm -rf /tmp/cache" }`
> [ Approve ]  [ Deny ]

Click **Approve** and the blocked call proceeds; click **Deny** and it's refused. The
message is rewritten to `✅ Approved by slack:you` (or `🛑 Denied`) so it can't be
actioned twice. The decision is recorded in the audit trail with `by = slack:<user>`.

## Security

- The bot token and signing secret are **secrets** — supply them via the environment
  (or your secret manager); they are never logged and never leave the process except
  as the `Authorization` header to Slack's API.
- The interactivity callback is the one route **not** behind the daemon API token
  (Slack can't send it). It's authenticated by the **Slack request signature**
  instead — verified over the raw request body with a 5-minute replay window, and it
  **fails closed** if no signing secret is configured. See [`SECURITY.md`](../SECURITY.md).
- It needs a URL Slack can reach. For local testing, front the daemon with a tunnel
  (e.g. `ngrok`); in production, your normal ingress. Keep the rest of the API behind
  your network boundary / the API token as usual.

## Notes

- A pending approval still respects the run's **time budget** and the **kill switch**:
  if the run is cancelled or runs out of time while waiting, the call is refused even
  if no one clicked. Slack is a transport, not a separate timeout.
- The webhook (`RISKKERNEL_APPROVAL_WEBHOOK`) and Slack can be enabled together — a
  pending approval fans out to both.
