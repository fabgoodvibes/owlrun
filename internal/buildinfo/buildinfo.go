// Package buildinfo holds build-time variables injected via ldflags.
// Both cmd/owlrun and internal/config import this to adapt behaviour
// based on the build variant (beta vs production).
package buildinfo

// Set at build time via:
//
//	-ldflags "-X github.com/fabgoodvibes/owlrun/internal/buildinfo.Version=v1.0.0
//	          -X github.com/fabgoodvibes/owlrun/internal/buildinfo.Network=production"
var (
	Version = "dev"
	Network = "beta" // "beta" or "production"
)

// IsBeta returns true for testnet/beta builds.
func IsBeta() bool { return Network == "beta" }
