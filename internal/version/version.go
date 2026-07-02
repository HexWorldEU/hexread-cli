// Package version exposes the CLI's build metadata (injected via -ldflags).
package version

// Build metadata, overridden at build time via:
//
//	-ldflags "-X github.com/HexWorldEU/hexread-cli/internal/version.Version=... -X ...Commit=... -X ...Date=..."
var (
	// Version is the hexread build version (git describe, or "dev" locally).
	Version = "dev"
	// Commit is the short git SHA the binary was built from.
	Commit = "none"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
)

// Info is the structured payload printed by `hexread version`. Engine/model details are
// deliberately not baked into the binary - the server reports them live per conversion.
type Info struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func Get(service string) Info {
	return Info{Service: service, Version: Version, Commit: Commit, Date: Date}
}
