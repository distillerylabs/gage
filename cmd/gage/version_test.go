package main

import (
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
