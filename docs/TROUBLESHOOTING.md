# Troubleshooting

The errors a new RiskKernel user actually hits, each as **symptom → cause →
fix**. If your problem isn't here, [open an issue](https://github.com/prashar32/riskkernel/issues)
— a confusing error is a docs bug as much as a code bug.

> **Run this first.** `riskkernel doctor` is the built-in diagnostics command. It
> checks your data dir, provider credential, default budget, API token, policy
> file, and probes a running daemon — and exits non-zero if anything hard-fails.
> Most of what's below, it catches before you hit it.
>
> ```bash
> riskkernel doctor
> ```

---

## Provider API key missing

**Symptom.** A model call (proxy, `riskkernel chat`, or the SDK) fails with:

```
anthropic: missing API key
openai: missing API key
```

`riskkernel doctor` flags it ahead of time:

```
⚠ default provider (anthropic)  — ANTHROPIC_API_KEY not set — model calls will fail
```

and `riskkernel serve` logs a startup warning:

```
ANTHROPIC_API_KEY is not set — model calls will fail until a key is provided
```

**Cause.** The default provider needs a key, and none is in the environment.
RiskKernel reads keys from the environment / `.env` only — it never stores them in
state.

**Fix.** Set the key for your default provider before starting the daemon:

```bash
export ANTHROPIC_API_KEY=sk-ant-...   # or OPENAI_API_KEY for OpenAI
```

If you scaffolded with `riskkernel init`, put it in the generated `.env` (the
daemon reads it). Local models via Ollama are key-free — set
`RISKKERNEL_DEFAULT_PROVIDER=ollama` and no key is needed.

---

## Provider API key invalid (or wrong provider)

**Symptom.** The key is set, but a proxy call returns **HTTP 502** with:

```json
{ "code": "provider_error", "message": "anthropic: invalid x-api-key (authentication_error, http 401)" }
```

The exact message is the upstream provider's, surfaced verbatim in the form
`anthropic: <message> (<type>, http <status>)` (same shape for `openai:`).

**Cause.** RiskKernel forwarded the call to the real provider with your key, and
the provider rejected it — a bad/expired key, the wrong provider's key, or a
model your account can't access (`http 401` / `403` / `invalid_request_error`).
The governor did its job; the failure is upstream.

**Fix.** Verify the key works against the provider directly, then confirm the
model id routes to the provider whose key you set — `claude-*` routes to
Anthropic, `gpt-*` / `o1` / `o3` to OpenAI. A `claude-*` model with only
`OPENAI_API_KEY` set will fail this way. `riskkernel chat "hi"` is the fastest
isolated check of the provider path.

---

## Port already in use (7070)

**Symptom.** `riskkernel serve` exits immediately with:

```
listen tcp :7070: bind: address already in use
```

**Cause.** Another process — often a RiskKernel daemon you already started — holds
port 7070 (the default daemon port).

**Fix.** Find and stop the other listener, or run on a different port:

```bash
lsof -i :7070            # see what's holding it
riskkernel doctor        # "daemon (:7070) — responding on /healthz" means one is already up
```

To move ports, set `RISKKERNEL_PORT` (and update `OPENAI_BASE_URL` /
`OTEL_EXPORTER_OTLP_ENDPOINT` to match). With Docker, change the host side of the
mapping, e.g. `-p 8070:7070`.

---

## A run was halted — HTTP 402 (this is expected, not a bug)

**Symptom.** A proxy call returns **HTTP 402** with a body and a header:

```json
{ "code": "dollar_budget_exceeded", "message": "run halted: dollar_budget_exceeded" }
```

```
X-RiskKernel-Halt-Reason: dollar_budget_exceeded
```

`riskkernel runs list` shows the run as `halted`.

**Cause.** This is RiskKernel **working as designed** — the headline feature, not
an error. The run hit one of its four budgets and the governor stopped it before
the next call. The `code` / `X-RiskKernel-Halt-Reason` is one of:

| Halt reason | Tripped budget |
|---|---|
| `dollar_budget_exceeded` | `dollars` — cumulative USD from the cost ledger |
| `token_budget_exceeded`  | `tokens` — prompt + completion across the run |
| `loop_budget_exceeded`   | `loops` — agent loop iterations / steps |
| `time_budget_exceeded`   | `seconds` — wall-clock duration |

Unconfigured, every run gets a **safe default budget** ($5 / 100 loops / 1 hour),
so a 402 on a brand-new setup usually means the safe default did its job on a
runaway loop. See [the budget contract](BUDGETS.md).

**Fix.** Decide whether the halt was correct. If it was a runaway loop, it just
saved you money — nothing to fix. If the limit was genuinely too low, **start a
new run with a bigger budget** (raise `RISKKERNEL_DEFAULT_DOLLARS` /
`_LOOPS` / `_TOKENS` / `_SECONDS`, set a per-run `budget`, or use a policy
bundle). A budget-halted run stays halted by design — it does not resume; that's
crash-resume's job (next entry), not the kill switch's.

---

## A killed run didn't resume / re-spent

**Symptom.** You `kill -9`'d the daemon mid-run and expected it to pick up where it
left off, but it looks like it started over (or didn't resume at all).

**Cause.** Resume is automatic on the next `riskkernel serve` — the daemon reloads
mid-flight runs from the durable store on startup. If you don't see it,
the run had no checkpoint yet, or the state file isn't the same one
(`RISKKERNEL_DATA_DIR` / the mounted volume differs between runs).

**Fix.** Restart with the **same** data dir / volume. On startup the daemon logs:

```
resumed runs from store  count=1
```

A resumed run enforces against what it had **already** spent — if it had burned
$4.20 of a $5 budget, it resumes at $4.20, never $0, so it can't overspend by
restarting. Re-attaching from the SDK is `rt.resume_run(run_id)`; over the API
it's `GET /v1/runs/{id}` + `GET /v1/checkpoints/{id}` reusing the same id. See
[the crash-resume guide](RESUME.md).

---

## Daemon refuses to start: schema is newer than the binary

**Symptom.** `riskkernel serve` exits at startup with:

```
storage: on-disk schema is newer than this binary; upgrade riskkernel (on-disk v5 > binary v4)
```

**Cause.** **Downgrade protection.** Your state file (SQLite or Postgres) was
written by a *newer* RiskKernel than the binary you're now running. Rather than
risk corrupting your data with an older binary, the daemon refuses to start. This
is intentional — see [`COMPATIBILITY.md`](../COMPATIBILITY.md).

**Fix.** Run a RiskKernel **at least as new** as the one that last touched the
state. Migrations are forward-only and run automatically on startup, so upgrading
is the supported path; there is no downgrade migration. If you genuinely need the
old binary, point it at a **fresh** data dir (`RISKKERNEL_DATA_DIR`) — but you'll
lose the existing runs/ledger/checkpoints.

---

## API returns 401 Unauthorized

**Symptom.** Requests to the daemon's API/proxy return **401** with:

```json
{ "code": "unauthorized", "message": "missing or invalid bearer token" }
```

even though the daemon is up and the provider key is fine.

**Cause.** You set `RISKKERNEL_API_TOKEN`, which turns on single-tenant bearer
auth on the API — and the request didn't send the token (or sent the wrong one).

**Fix.** Send the bearer token on every request:

```bash
curl -H "Authorization: Bearer $RISKKERNEL_API_TOKEN" http://localhost:7070/v1/...
```

The Python SDK reads `RISKKERNEL_API_TOKEN` from the environment, or takes it as
`Runtime(token=...)`. If you *didn't* mean to require auth, the daemon warns at
startup when it's unset —

```
RISKKERNEL_API_TOKEN is not set — the API is unauthenticated; do not expose this port to an untrusted network
```

— which is the opposite case: fine on localhost, dangerous on an exposed port.
Either set a token, or keep the port local.

---

## OTLP endpoint unreachable / no spans showing up

**Symptom.** You pointed RiskKernel at an OTLP backend but no spans arrive in
Grafana/Tempo/SigNoz/Honeycomb/Datadog, or the daemon logs export errors.

**Cause.** OTel export is **off until you configure an endpoint** — RiskKernel
never emits telemetry on its own. If it's on, the usual culprits are the wrong
endpoint URL, the wrong protocol (`grpc` vs `http`), or the collector not
listening.

**Fix.** Set the endpoint (and protocol if not gRPC) before starting the daemon:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc   # default; use "http" for OTLP/HTTP
```

When export is enabled the daemon logs it at startup:

```
otel export enabled  endpoint=http://localhost:4317 protocol=grpc
```

If that line is missing, the endpoint env var isn't set in the daemon's
environment. RiskKernel reads `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` first, then
`OTEL_EXPORTER_OTLP_ENDPOINT`. See [`METRICS.md`](METRICS.md) for the local
`/metrics` scrape, which needs no exporter at all.

---

## OTLP ingress returns 400 Bad Request

**Symptom.** An app exporting traces *into* RiskKernel (Surface 3 ingress) gets:

```
invalid OTLP protobuf: ...
invalid OTLP JSON: ...
```

**Cause.** The body sent to `POST /v1/traces` wasn't valid OTLP in the declared
encoding (a content-type / payload mismatch — RiskKernel accepts
`application/x-protobuf`, the OTLP default, and JSON). Note the ingress receiver
is **off** unless `RISKKERNEL_OTEL_INGRESS_ENABLED` is truthy.

**Fix.** Enable ingress and make sure your exporter targets the RiskKernel
endpoint with a matching content-type:

```bash
export RISKKERNEL_OTEL_INGRESS_ENABLED=1
# in the producing app:
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:7070
```

See [`OTLP_INGRESS.md`](OTLP_INGRESS.md) for the spans RiskKernel meters and how
runs are attributed.

---

## Unknown provider / model not routing

**Symptom.** A proxy call returns **HTTP 400**:

```json
{ "code": "unknown_provider", "message": "..." }
```

**Cause.** The `model` in the request didn't route to a configured provider.
RiskKernel routes by model-id prefix — `claude-*` → Anthropic, `gpt-*` / `o1` /
`o3` → OpenAI — and anything else falls through to the default provider, which may
not be registered.

**Fix.** Use a model id that routes to a provider you've configured a key for, or
set `RISKKERNEL_DEFAULT_PROVIDER` to the one you want unprefixed models to use.
For providers RiskKernel doesn't implement natively, front them with LiteLLM as an
upstream forwarder.

---

## Where to look next

- [`BUDGETS.md`](BUDGETS.md) — the budget contract: dimensions, precedence, safe
  defaults, and exact halt semantics.
- [`RESUME.md`](RESUME.md) — crash-resume mechanics and what's restored.
- [`METRICS.md`](METRICS.md) / [`OTLP_INGRESS.md`](OTLP_INGRESS.md) — observability.
- [`POSTGRES.md`](POSTGRES.md) — the opt-in Postgres backend.
- [`COMPATIBILITY.md`](../COMPATIBILITY.md) — what's stable across versions and the
  downgrade-protection policy.
