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

The tap ([prashar32/homebrew-riskkernel](https://github.com/prashar32/homebrew-riskkernel))
is live, with the formula tracking the latest release.

## How it works

On every version tag, the [`Publish Homebrew formula`](../.github/workflows/homebrew-publish.yml)
workflow downloads the release's `checksums.txt`, renders the formula with
[`scripts/gen-homebrew-formula.sh`](../scripts/gen-homebrew-formula.sh) (a binary
formula with a per-OS/arch `url` + `sha256` taken from the GoReleaser archives), and
commits it to the tap repo as `Formula/riskkernel.rb`. It mirrors how the Python and
TypeScript SDKs publish on a tag.

## Keeping the formula current

The tap repo **`prashar32/homebrew-riskkernel`** exists and is seeded with the
current formula, so `brew install` works today. There are two ways to update it on
future releases:

- **Hands-free (recommended):** add an Actions secret **`HOMEBREW_TAP_TOKEN`** on the
  `prashar32/riskkernel` repo — a fine-grained PAT scoped to the `homebrew-riskkernel`
  repo with **Contents: read and write**. The `Publish Homebrew formula` workflow then
  pushes the formula automatically on every `v*` tag. Without the secret the workflow
  logs a notice and exits 0 (it never blocks a release).
- **Manual:** run the generator against a release's checksums and push to the tap:
  ```bash
  gh release download vX.Y.Z --repo prashar32/riskkernel --pattern checksums.txt --dir /tmp/r
  scripts/gen-homebrew-formula.sh X.Y.Z /tmp/r/checksums.txt > Formula/riskkernel.rb  # in the tap repo
  ```

> On the eventual org transfer (`prashar32` → a `riskkernel` org), move the tap repo
> and re-point the `TAP_REPO` in the workflow, the same way the PyPI/npm trusted
> publishers move.
