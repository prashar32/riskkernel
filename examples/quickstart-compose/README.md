# Quickstart (docker compose) — watch RiskKernel kill a runaway agent

The fastest way to see RiskKernel work: **one command**, no API key, no local Go or
Python. `docker compose up` brings up the governance daemon and a tiny agent that
loops forever — and you watch the deterministic loop budget hard-stop it.

```bash
cd examples/quickstart-compose
docker compose up
```

You'll see the agent's calls go through, then get cut off:

```
  call 1  -> 200 OK    tokens=1600  cost=$...
  call 2  -> 200 OK    tokens=1600  cost=$...
  call 3  -> 200 OK    tokens=1600  cost=$...
  call 4  -> 200 OK    tokens=1600  cost=$...
  call 5  -> 200 OK    tokens=1600  cost=$...

  call 6  -> HTTP 402   RiskKernel HALTED the run:
      {"code":"loop_budget_exceeded","message":"..."}

  The kill came from RiskKernel's deterministic loop budget — the
  agent script never chose to stop.
```

`docker compose up` exits when the agent is halted. Tear it down with:

```bash
docker compose down
```

## What's running

Three small services (see [`docker-compose.yml`](docker-compose.yml)):

| Service | Image | Role |
|---|---|---|
| `mock-llm` | `nginx:alpine` | A stand-in OpenAI-compatible upstream that returns one canned completion with token usage — so the demo needs **no real provider and no API key**. |
| `riskkernel` | `ghcr.io/prashar32/riskkernel:latest` | The governance daemon. A hard **loop budget of 5** (`RISKKERNEL_DEFAULT_LOOPS=5`); the other dimensions are unlimited so the loop budget is the sole, unambiguous enforcer. Forwards model calls to the mock. |
| `sample-agent` | `curlimages/curl` | A minimal "agent" — [`agent.sh`](agent.sh) just loops through the proxy under one run id and never stops on its own. RiskKernel stops it. |

The agent changes nothing about how it calls the model — it's a plain
OpenAI-style `POST /v1/chat/completions`. The budget enforcement, the metering, and
the 402 kill all come from RiskKernel sitting in front.

## Use it for real

Swap the mock for a real provider and govern your own app — still one env var on
the app side:

1. Give the daemon a real key and drop the mock override. In `docker-compose.yml`,
   set `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY`) on the `riskkernel` service and
   remove the `RISKKERNEL_OPENAI_BASE_URL` line (and the `mock-llm` service).
2. Pick the budget you want, e.g. `RISKKERNEL_DEFAULT_DOLLARS=5` for a hard $5
   per-run ceiling alongside (or instead of) the loop cap.
3. Point your existing app at the proxy — one env var, no code change:
   `OPENAI_BASE_URL=http://localhost:7070/v1` (or `ANTHROPIC_BASE_URL=http://localhost:7070`).

Your keys stay in the daemon's environment, never in app config, and nothing is
sent anywhere except the provider call you already make. See the repo
[README](../../README.md) for budgets-as-config, crash-resume, approvals, and the
OpenTelemetry integration.
