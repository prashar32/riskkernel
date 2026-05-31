# Contributing to RiskKernel

Thanks for considering a contribution! This guide gets you from clone to merged
PR, and lays out the few rules that keep RiskKernel trustworthy.

New here? Skim [`ARCHITECTURE.md`](ARCHITECTURE.md) first — it maps the codebase
and answers *"where do I make this change?"*. Good entry points are issues
labeled [`good first issue`](https://github.com/prashar32/riskkernel/labels/good%20first%20issue).

## Getting started

Requires **Go 1.25+** (matches the `go` directive in `go.mod`; the daemon is
pure-Go, no cgo) and, for SDK work, Python 3.9+.

```bash
git clone https://github.com/prashar32/riskkernel
cd riskkernel
make build        # build the static binary
make check        # gofmt check + go vet + race tests (run this before every PR)
make test         # just the race tests
make sdk-test     # Python SDK tests (stdlib only)
make help         # all targets
./riskkernel serve   # run the daemon on :7070
```

## How to contribute (GitHub Flow)

We use a single `main` branch with short-lived feature branches.

1. **Fork** the repo and create a branch off `main` (`fix/...`, `feat/...`).
2. Make your change, with tests. Keep commits focused; use
   [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`,
   `docs:`, `chore:`, `ci:`, …).
3. Run `make check` (and `make sdk-test` if you touched the SDK).
4. Update [`CHANGELOG.md`](CHANGELOG.md) under `## [Unreleased]` for any
   user-facing change.
5. Open a **PR against `main`**. CI must pass: **`build & test`** and **`CodeQL`**
   are required checks, and a maintainer review is required before merge.
6. A maintainer reviews and merges (squash). Releases are cut from `main` by tag.

For anything non-trivial, open an issue first so we can agree on the approach.

## Where to make your change

See the **"I want to… — where do I code?"** table in
[`ARCHITECTURE.md`](ARCHITECTURE.md). The short version: providers →
`internal/provider`; enforcement → `internal/governor`; HTTP endpoints →
`internal/httpapi` (+ `api/v1`); storage backends → implement `storage.Store`;
DB changes → a **new** forward-only migration in
`internal/storage/migrations/`; SDK → `sdks/python/riskkernel`.

## The rules that keep RiskKernel trustworthy

### The `internal/` boundary (hard rule)
The only public surfaces are `api/v1/` (the versioned contract + OTel attribute
set), `pkg/` (public Go packages), and the SDKs under `sdks/`. **Everything under
`internal/` is private** and no external consumer may import it — Go's `internal/`
convention enforces this; don't work around it. Need something from `internal/`?
Promote a minimal, stable interface into `pkg/` and discuss it first.

### The deterministic/LLM split
All enforcement — budgets, kill switches, approval gating, tool-permission checks,
retries, routing, state transitions, checkpoint/resume — is **deterministic Go and
only Go**. An LLM is never in the enforcement path. PRs that put a governance
decision behind a model call will be declined.

### The no-telemetry promise
No phone-home, no analytics, no beacons — ever. Outbound network is only allowed
in `internal/provider/` (LLM providers), `internal/otel/` (user-configured OTLP),
and `internal/approval/` (the user-configured approval webhook). PRs adding
outbound calls elsewhere will be declined. See [`SECURITY.md`](SECURITY.md).

### The honesty constraint
This is a systems/reliability product, not an ML/research product. Don't add
features that need claims we can't defend from fundamentals ("the AI figures it
out"). Make it deterministic, make it human-in-the-loop, or cut it.

## Checklist before you open a PR

- [ ] `make check` passes (gofmt, `go vet`, race tests); `make sdk-test` if SDK changed.
- [ ] Tests added/updated — the governor, approval gate, and checkpoint manager are safety-critical and get the most coverage.
- [ ] `CHANGELOG.md` updated for user-facing changes.
- [ ] `api/v1/` changes are additive (no breaking the contract).
- [ ] No new outbound network calls outside `provider` / `otel` / `approval`.
- [ ] No enforcement decision placed behind an LLM.
- [ ] New dependencies justified (fewer deps = a more auditable trust surface).

## Reporting security issues

Please report vulnerabilities **privately** — do not open a public issue. See
[`SECURITY.md`](SECURITY.md). By participating you agree to the
[Code of Conduct](CODE_OF_CONDUCT.md).
