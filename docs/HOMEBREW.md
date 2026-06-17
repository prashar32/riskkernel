# Homebrew

Install RiskKernel as a prebuilt binary on macOS or Linux via Homebrew:

```bash
brew install prashar32/riskkernel/riskkernel
# or:
brew tap prashar32/riskkernel
brew install riskkernel
```

This pulls the signed release binary for your OS/arch (no compile). Upgrade with
`brew upgrade riskkernel`.

> **Status:** the publishing automation is in the repo, but `brew install` works only
> once the tap repo and token are set up (one-time maintainer step, below). Until
> then, install via `go install`, `docker run`, or the release binaries — see the
> [README quickstart](../README.md#quickstart-60-seconds).

## How it works

On every version tag, the [`Publish Homebrew formula`](../.github/workflows/homebrew-publish.yml)
workflow downloads the release's `checksums.txt`, renders the formula with
[`scripts/gen-homebrew-formula.sh`](../scripts/gen-homebrew-formula.sh) (a binary
formula with a per-OS/arch `url` + `sha256` taken from the GoReleaser archives), and
commits it to the tap repo as `Formula/riskkernel.rb`. It mirrors how the Python and
TypeScript SDKs publish on a tag.

## Maintainer setup (one-time, to activate)

The workflow is **inert until two things exist** — without them it logs a notice and
exits 0, so it never blocks a release:

1. **Create the tap repo.** A *public* repo named **`homebrew-riskkernel`** under the
   same owner (`prashar32`). Homebrew requires the `homebrew-` name prefix; it maps to
   the tap `prashar32/riskkernel`. It can start empty — the workflow writes
   `Formula/riskkernel.rb`.
2. **Add the token secret.** On the `prashar32/riskkernel` repo, add an Actions secret
   **`HOMEBREW_TAP_TOKEN`**: a fine-grained personal access token scoped to the
   `homebrew-riskkernel` repo with **Contents: read and write**.

Then publish the current release: re-run the **Publish Homebrew formula** workflow
(Actions → *Run workflow*), or just cut the next release — it runs automatically on
each `v*` tag.

> On the eventual org transfer (`prashar32` → a `riskkernel` org), move the tap repo
> and re-point the `TAP_REPO` in the workflow, the same way the PyPI/npm trusted
> publishers move.
