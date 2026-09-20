package main

import (
	"fmt"
	"runtime/debug"
)

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

// resolveBuildInfo fills in what the linker flags left unset from the build
// info Go embeds in every binary. `go install ...@vX.Y.Z` builds without the
// Makefile's -X flags, but still records the module version; a checkout
// build records the VCS revision. Anything the linker set, including the
// Makefile's explicit "dev", always wins; only unset values fall back.
func resolveBuildInfo(version, commit string, read func() (*debug.BuildInfo, bool)) BuildInfo {
	b := BuildInfo{Version: version, Commit: commit}
	info, ok := read()
	if !ok {
		return b
	}
	if b.Version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		b.Version = info.Main.Version
	}
	if b.Commit == "" {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				b.Commit = s.Value[:7]
			}
		}
	}
	return b
}

// Set via -X main.version=... / -X main.commit=... at build time; see
// the Makefile.
var (
	version = ""
	commit  = ""
)
