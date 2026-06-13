# Security Policy

RiskKernel is a reliability and governance daemon for AI agents. It runs on infrastructure you control, handles your provider API keys, and records what your agents do. Security and trust are the product, so this posture is part of the contract — not a footnote.

## The no-telemetry promise

**RiskKernel never phones home. There is no analytics, no usage beacon, no crash reporter, no license check, no "anonymous metrics."** Nothing leaves your machine except the LLM API calls *you* configure, sent to the providers *you* choose, with *your* keys.

This is verifiable, and we intend for you to verify it:

- **Read the code.** All network egress originates in three places, each going only where *you* point it: `internal/provider/` (calls to the LLM providers you configure), `internal/otel/` (spans, only when you set an OTLP endpoint), and `internal/approval/` (the approval webhook when you set `RISKKERNEL_APPROVAL_WEBHOOK`, and Slack's API when you set `RISKKERNEL_APPROVAL_SLACK_BOT_TOKEN`). The webhook/Slack payload carries the pending approval's metadata (run id, tool, side effect, arguments) — never provider keys or other secrets. There is no other outbound HTTP client in the codebase. The Slack interactivity callback (`/v1/integrations/slack/interactions`) is the one inbound route not guarded by the API token — it is authenticated instead by the Slack request signature (`RISKKERNEL_APPROVAL_SLACK_SIGNING_SECRET`), verified over the raw body with a replay window, and fails closed if no signing secret is set.
- **Watch the wire.** Run RiskKernel under `tcpdump`/`Little Snitch`/`mitmproxy` and confirm the only destinations are your configured provider, OTLP, and (if set) approval-webhook / Slack endpoints.
- **Build gates.** CI fails if a disallowed network import appears outside the provider/otel packages.

Any future "you're N versions behind" hint is a local startup log line comparing against a build-time constant — it makes no network call. An optional `--share-anonymous-usage` flag may exist someday; it will be **OFF by default** and clearly documented.

## How secrets are handled

- Provider API keys come **only** from environment variables, a `.env` file, or the OS keyring.
- Keys are **never** written to the SQLite state, **never** logged, and **never** sent anywhere but the provider API they authenticate.
- RiskKernel mints local-only virtual keys (`rk_live_…`) that are swapped for real keys at egress, so your application code and logs never carry the real secret.

## Supply chain

- Release Docker images are **cosign-signed** from day one.
- `govulncheck` runs in CI on every change.
- The dependency graph is pinned and kept minimal — fewer dependencies means an auditable trust surface.

## Reporting a vulnerability

Until a dedicated channel is published, email the maintainer (see the repository profile) with `[SECURITY]` in the subject. Please do **not** open a public issue for an exploitable vulnerability. We aim to acknowledge within 72 hours.

## Scope notes for v0.1

v0.1 is **single-tenant**: one API token, no multi-user auth/RBAC/SSO (use oauth2-proxy / Authelia in front of it if you need that). Treat the daemon's admin surface as you would any local infrastructure control plane — do not expose `:7070` to an untrusted network without an authenticating proxy.
