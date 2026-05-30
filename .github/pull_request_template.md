<!-- Thanks for contributing to RiskKernel. -->

## What & why

<!-- What does this change, and what problem does it solve? Link any issue. -->

Closes #

## Checklist

- [ ] `go test ./...` and `go vet ./...` pass; `gofmt` clean.
- [ ] Tests added/updated (the governor, approval gate, and checkpoint manager are safety-critical).
- [ ] `CHANGELOG.md` updated for any user-facing change.
- [ ] No new outbound network calls outside `internal/provider` / `internal/otel` (no telemetry).
- [ ] No enforcement decision placed behind an LLM (deterministic core stays deterministic).
- [ ] Public surface (`api/v1`, `pkg/`, SDKs) only — no new external use of `internal/`.
- [ ] Conventional Commit title (`feat:`, `fix:`, `docs:`, `chore:`, …).

## Notes for reviewers

<!-- Anything that needs attention, trade-offs, follow-ups. -->
