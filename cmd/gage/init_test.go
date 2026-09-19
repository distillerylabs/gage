package main

import (
	"strings"
	"testing"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

func TestResolveDeviceNameExplicitOverridesHostname(t *testing.T) {
	got, err := resolveDeviceName("custom-device", "andrews-macbook-pro.local")
	if err != nil {
		t.Fatal(err)
	}
	if got != "custom-device" {
		t.Errorf("resolveDeviceName = %q, want %q", got, "custom-device")
	}
}

func TestResolveDeviceNameFallsBackToNormalizedHostname(t *testing.T) {
	got, err := resolveDeviceName("", "Andrews-MacBook-Pro.local")
	if err != nil {
		t.Fatal(err)
	}
	if got != "andrews-macbook-pro" {
		t.Errorf("resolveDeviceName = %q, want %q", got, "andrews-macbook-pro")
	}
}

func TestResolveDeviceNameExplicitFailingAllowlistIsUsageError(t *testing.T) {
	_, err := resolveDeviceName("../../etc/x", "irrelevant")
	if err == nil {
		t.Fatal("expected an error")
	}
	if exitcode.CodeOf(err) != exitcode.Usage {
		t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
	}
}

// TestResolveDeviceNameHostnameNormalizesToNothingPromptsRatherThanInventing
// is the M1 plan's "a hostname that normalizes to nothing usable causes
// gage to prompt for a device name rather than inventing one." In
// one-shot mode there's no free-text prompt to drive (Prompter has no
// such method yet), so "prompting" means failing clearly with the flag
// to supply, instead of silently fabricating something like "device" or
// an empty string.
func TestResolveDeviceNameHostnameNormalizesToNothingPromptsRatherThanInventing(t *testing.T) {
	for _, hostname := range []string{"", "...", "!!!", "---"} {
		t.Run(hostname, func(t *testing.T) {
			_, err := resolveDeviceName("", hostname)
			if err == nil {
				t.Fatalf("expected an error for hostname %q", hostname)
			}
			if exitcode.CodeOf(err) != exitcode.Usage {
				t.Errorf("CodeOf(err) = %v, want Usage", exitcode.CodeOf(err))
			}
			if !strings.Contains(err.Error(), "--device") {
				t.Errorf("error doesn't name the --device flag: %v", err)
			}
		})
	}
}
