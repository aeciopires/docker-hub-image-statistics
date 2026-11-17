// Package version exposes build metadata injected at link time.
package version

import "runtime"

// These variables are overridden at build time through -ldflags "-X ...".
var (
	// Version is the semantic version of the build.
	Version = "dev"
	// Commit is the git commit SHA of the build.
	Commit = "unknown"
	// Date is the build date in RFC 3339 format.
	Date = "unknown"
)

// Info groups build metadata for presentation.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
}

// Get returns the current build metadata.
func Get() Info {
	return Info{Version: Version, Commit: Commit, Date: Date, GoVersion: runtime.Version()}
}
