// Package version exposes build identity. Values are overridable at build time
// via -ldflags so releases stamp the real version/commit. There is NO network
// version check here — any "you're N versions behind" hint is a local log line
// only (see SECURITY.md / COMPATIBILITY.md).
package version

// These are set via -ldflags "-X github.com/prashar32/riskkernel/internal/version.Version=..."
var (
	// Version is the semantic version of this build.
	Version = "0.8.1-dev"
	// Commit is the git commit this binary was built from.
	Commit = "unknown"
	// Date is the build date (RFC3339), stamped at release.
	Date = "unknown"
)

// String returns a human-readable build identifier.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
