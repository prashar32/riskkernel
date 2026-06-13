# Policy-as-code

A **policy bundle** is a named, reusable set of a default budget, a tool allowlist,
and approval rules. A run references one by name instead of inlining its limits, so
the policy lives in version control and is **reviewed in PRs** — not buried in
application code. Evaluation is deterministic; no LLM is ever consulted.

Two ways to register a bundle, same model behind both:

- **`riskkernel.yaml`** (policy-as-code) — a file applied on startup.
- **`POST /v1/policies`** — register/update at runtime over the API.

## riskkernel.yaml

```yaml
schemaVersion: 1
policies:
  - name: developer
    budget: { tokens: 200000, dollars: 5.00, loops: 50, seconds: 1800 }
    toolAllowlist: [ "mcp://github", "mcp://filesystem", "mcp://shell" ]
    approvalPolicy:
      requireFor:
        - { tool: "mcp://shell" }
        - { sideEffect: "*write*" }
```

Point the daemon at it and the bundles are registered on boot:

```bash
export RISKKERNEL_POLICY_FILE=riskkernel.yaml
riskkernel serve
# → "policy bundles registered" file=riskkernel.yaml count=1
```

A malformed file (bad `schemaVersion`, an unknown field, a duplicate or missing
name, a negative budget) **fails startup** rather than booting on a stale or partial
policy. Validate before you ship it:

```bash
riskkernel policy validate riskkernel.yaml
# ✓ riskkernel.yaml — 1 policy bundle(s), schemaVersion 1
#   • developer  (budget: $5.00 / 50 loops / 200000 tokens / 1800s · allowlist: 3 · approval rules: 2)
```

A full example: [`examples/policy/riskkernel.yaml`](../examples/policy/riskkernel.yaml).

## Reference a bundle from a run

```bash
curl -X POST localhost:7070/v1/runs -d '{"policyRef":"developer"}'
# the run inherits the bundle's budget
curl -X POST localhost:7070/v1/runs -d '{"policyRef":"developer","budget":{"loops":10}}'
# an inline budget overrides the bundle field-by-field (loops here; the rest stay)
```

## Dry-run a policy against a recorded run

Before adopting a policy, see exactly what it *would* have done to a run that already
happened — no changes, no re-execution:

```bash
riskkernel policy dry-run riskkernel.yaml <run-id> developer
```
```
Dry-run: policy "developer" vs run 4f3c…
  budget:    WOULD HALT — dollar_budget_exceeded ($6.4200 spent ≥ $5.00 budget)
  allowlist: 1 of 7 tool calls blocked
      ✗ step 4  mcp://email
  approval:  2 of 7 tool calls would require sign-off
      ⏸ step 3  mcp://shell (exec)
      ⏸ step 6  mcp://filesystem (write)
```

It replays the run's recorded cost ledger and tool calls against the bundle and
reports: whether the **budget** would have halted it (and on which dimension), which
tool calls fall outside the **allowlist**, and which would have required **approval**.
A trust-builder for tightening a policy without breaking a working agent.

## Fields

| Field | Meaning |
|---|---|
| `budget` | Hard per-run limits (`tokens`, `dollars`, `loops`, `seconds`). Any omitted dimension is unlimited. |
| `toolAllowlist` | Tools a run may call. Empty/omitted means all tools are allowed. |
| `approvalPolicy.requireFor` | Match rules; a call needs human approval if it matches **any** — by exact `tool` or by `sideEffect` glob (e.g. `*write*`). |

The same fields are the `POST /v1/policies` body — see [`api/v1/openapi.yaml`](../api/v1/openapi.yaml).

## Per-run enforcement

A run created under a bundle (via `policyRef`) is governed by that bundle, not just
the daemon-global config:

- **Budget** — applied to the run (an inline `budget` overrides it field-by-field).
- **Tool allowlist** — the MCP gateway enforces the bundle's `toolAllowlist` for that
  run's `tools/call`s: a tool outside it is blocked, even if the global allowlist
  would allow it. An empty bundle allowlist falls back to the global one.
- **Approval rules** — the bundle's `approvalPolicy` rules apply to that run *on top
  of* the global fail-safe gating (side-effecting tools still gate; the bundle can
  add a requirement, e.g. naming a normally read-only tool). A run's bundle can only
  *add* gating, never silently drop it.

The run's `policyRef` is persisted, so enforcement continues after a daemon restart.
Runs created without a `policyRef` keep using the global config.
