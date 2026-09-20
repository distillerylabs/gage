package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionFlagPrintsVersionAndCommit(t *testing.T) {
	res := runCLI(t, []string{"--version"}, "")
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "v1.2.3") {
		t.Errorf("--version output missing the version tag: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "abcdef1") {
		t.Errorf("--version output missing the commit: %q", res.Stdout)
	}
}

func TestVersionStringUntaggedBuild(t *testing.T) {
	b := BuildInfo{Version: "dev", Commit: "abcdef1"}
	s := b.String()
	if !strings.Contains(s, "dev") || !strings.Contains(s, "abcdef1") {
		t.Errorf("BuildInfo.String() = %q, want it to mention dev and the commit", s)
	}
}

func TestVersionStringTaggedBuild(t *testing.T) {
	b := BuildInfo{Version: "v2.0.0", Commit: "deadbee"}
	s := b.String()
	if !strings.Contains(s, "v2.0.0") || !strings.Contains(s, "deadbee") {
		t.Errorf("BuildInfo.String() = %q, want it to mention the tag and the commit", s)
	}
}

func fakeBuildInfo(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: mainVersion}, Settings: settings}, true
	}
}

// go install ...@vX.Y.Z carries no ldflags, but the module version is
// embedded in the binary.
func TestResolveBuildInfoFallsBackToModuleVersion(t *testing.T) {
	got := resolveBuildInfo("", "", fakeBuildInfo("v0.3.0"))
	if got.Version != "v0.3.0" {
		t.Errorf("Version = %q, want v0.3.0", got.Version)
	}
}

func TestResolveBuildInfoFallsBackToVCSRevision(t *testing.T) {
	got := resolveBuildInfo("", "", fakeBuildInfo("(devel)",
		debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}))
	if got.Version != "" {
		t.Errorf("Version = %q, want unset: a (devel) module version is not a release", got.Version)
	}
	if got.Commit != "0123456" {
		t.Errorf("Commit = %q, want the 7-char vcs.revision", got.Commit)
	}
}

// Link-time values (the Makefile's -X flags) always win.
func TestResolveBuildInfoPrefersLinkerValues(t *testing.T) {
	got := resolveBuildInfo("v9.9.9", "cafef00", fakeBuildInfo("v0.3.0",
		debug.BuildSetting{Key: "vcs.revision", Value: "0123456789abcdef"}))
	if got.Version != "v9.9.9" || got.Commit != "cafef00" {
		t.Errorf("got %+v, want linker-provided values untouched", got)
	}
}

func TestResolveBuildInfoWithoutEmbeddedInfo(t *testing.T) {
	got := resolveBuildInfo("", "", func() (*debug.BuildInfo, bool) { return nil, false })
	if got.Version != "" || got.Commit != "" {
		t.Errorf("got %+v, want both unset", got)
	}
}

// The Makefile's explicit "dev" (no tag) must not be replaced by the
// pseudo-version Go embeds for a checkout build.
func TestResolveBuildInfoKeepsExplicitDev(t *testing.T) {
	got := resolveBuildInfo("dev", "abcdef1", fakeBuildInfo("v0.0.0-20260919221641-425425136e46+dirty"))
	if got.Version != "dev" {
		t.Errorf("Version = %q, want the linker's dev", got.Version)
	}
}
