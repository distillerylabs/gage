package main

import "fmt"

// BuildInfo is version metadata injected at link time via -X ldflags in
// the Makefile. Version is "dev" for an untagged build; on a tagged
// build it's the tag itself.
type BuildInfo struct {
	Version string
	Commit  string
}

// String is what `gage --version` prints.
func (b BuildInfo) String() string {
	version := b.Version
	if version == "" {
		version = "dev"
	}
	commit := b.Commit
	if commit == "" {
		commit = "unknown"
	}
	return fmt.Sprintf("gage %s (commit %s)", version, commit)
}

// Set via -X main.version=... / -X main.commit=... at build time; see
// the Makefile.
var (
	version = "dev"
	commit  = "unknown"
)
