#!/usr/bin/env bash
# Generates the Homebrew formula for riskkernel from a release's checksums.txt.
# Kept as a standalone script so it can be run/verified locally, not just in CI.
#
# Usage: scripts/gen-homebrew-formula.sh <version-without-v> <path/to/checksums.txt>
# Prints the formula (Formula/riskkernel.rb) to stdout.
set -euo pipefail

version="${1:?usage: gen-homebrew-formula.sh <version> <checksums.txt>}"
checksums="${2:?usage: gen-homebrew-formula.sh <version> <checksums.txt>}"
base="https://github.com/prashar32/riskkernel/releases/download/v${version}"

# sha256 of a named release asset, from goreleaser's "<sha>  <file>" checksums.txt.
sha() {
	local got
	got="$(awk -v f="$1" '$2 == f {print $1}' "$checksums")"
	if [ -z "$got" ]; then
		echo "gen-homebrew-formula: no checksum for $1 in $checksums" >&2
		exit 1
	fi
	printf '%s' "$got"
}

darwin_arm="$(sha "riskkernel_${version}_darwin_arm64.tar.gz")"
darwin_amd="$(sha "riskkernel_${version}_darwin_amd64.tar.gz")"
linux_arm="$(sha "riskkernel_${version}_linux_arm64.tar.gz")"
linux_amd="$(sha "riskkernel_${version}_linux_amd64.tar.gz")"

cat <<EOF
class Riskkernel < Formula
  desc "Self-hosted reliability runtime for AI agents (budgets, approvals, crash-resume)"
  homepage "https://github.com/prashar32/riskkernel"
  version "${version}"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "${base}/riskkernel_${version}_darwin_arm64.tar.gz"
      sha256 "${darwin_arm}"
    end
    on_intel do
      url "${base}/riskkernel_${version}_darwin_amd64.tar.gz"
      sha256 "${darwin_amd}"
    end
  end

  on_linux do
    on_arm do
      url "${base}/riskkernel_${version}_linux_arm64.tar.gz"
      sha256 "${linux_arm}"
    end
    on_intel do
      url "${base}/riskkernel_${version}_linux_amd64.tar.gz"
      sha256 "${linux_amd}"
    end
  end

  def install
    bin.install "riskkernel"
  end

  test do
    assert_match "riskkernel", shell_output("#{bin}/riskkernel version")
  end
end
EOF
