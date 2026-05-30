# Contributing to RiskKernel

Thanks for considering a contribution. A few rules keep RiskKernel trustworthy and
keep its public contract stable.

## The internal boundary (hard rule)

RiskKernel is the substrate that future products (and your own code) build on. The
only public surfaces are:

- `api/v1/` — the versioned REST/gRPC contract + OTel GenAI attribute set
- `pkg/` — public Go packages, including `pkg/plugin/` interfaces
- the SDKs under `sdks/`

**Everything under `internal/` is private.** No external consumer — including the
future company-builder — may import `internal/` packages. This is enforced by Go's
`internal/` convention; do not work around it. If you need something from
`internal/`, the right move is to promote a stable, minimal interface into `pkg/`
and discuss it first.

## The deterministic/LLM split

All enforcement logic — budgets, kill switches, approval gating, tool-permission
checks, retries, routing, state transitions, checkpoint/resume — is **deterministic
Go code and only Go code**. An LLM is never in the enforcement path. PRs that put
governance decisions behind a model call will be declined.

## The no-telemetry promise

No phone-home, no analytics, no beacons — ever. Network egress is only allowed in
`internal/provider/` (LLM providers) and `internal/otel/` (user-configured OTLP).
PRs adding outbound network calls elsewhere will be declined. See `SECURITY.md`.

## The honesty constraint

This is a systems/reliability product, not an ML/research product. Don't add
features that require claims we can't defend from fundamentals (RAG research,
eval-science, "the AI figures it out"). Make it deterministic, make it
human-in-the-loop, or cut it.

## Mechanics

- Conventional Commits for messages (`feat:`, `fix:`, `docs:`, `chore:`, …).
- Every user-facing change updates `CHANGELOG.md`.
- Changes to `api/v1/` must pass the contract-breaking-change check.
- `go test ./...` and `go vet ./...` must pass. The governor, approval gate, and
  checkpoint manager are safety-critical — they get the most test coverage.
- Justify every new dependency; fewer deps = a more auditable trust surface.
