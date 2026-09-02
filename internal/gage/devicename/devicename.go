// Package devicename normalizes and validates the device names gage uses
// to label recipients and identity files: lowercase hostname-derived
// strings restricted to a small filename-safe character set. See "Local
// identity storage" in the design doc and Q-DEVICE-NAME.
package devicename

import "strings"

// MaxLength caps a device name's length after normalization or on
// validation of an explicitly given name.
const MaxLength = 63

// charset is the allowlist every device name — normalized or
// explicitly given — must satisfy. It deliberately excludes any path
// separator ('/' on all platforms, '\' and ':' on Windows) so a name
// read back from a vault's committed, git-writable config.toml can never
// be used to escape the directory it's joined into. See Q-DEVICE-NAME.
func allowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '.' || r == '_' || r == '-':
		return true
	default:
		return false
	}
}

// Normalize derives a device name from a hostname: lowercased, truncated
// at the first '.', any character outside the allowlist replaced with
// '-' (runs of replacements collapsed to one), and length-capped. The
// second return value is false if nothing usable survives — callers
// should prompt for an explicit name rather than inventing one.
func Normalize(hostname string) (string, bool) {
	h := strings.ToLower(hostname)
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}

	var b strings.Builder
	lastWasDash := false
	for _, r := range h {
		switch {
		case allowed(r) && r != '-':
			b.WriteRune(r)
			lastWasDash = false
		case r == '-':
			if !lastWasDash {
				b.WriteByte('-')
			}
			lastWasDash = true
		default:
			if !lastWasDash {
				b.WriteByte('-')
			}
			lastWasDash = true
		}
	}

	name := strings.Trim(b.String(), "-")
	if len(name) > MaxLength {
		name = strings.TrimRight(name[:MaxLength], "-")
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// Valid reports whether name is safe to use as-is: non-empty, within
// MaxLength, and built entirely from the same allowlist Normalize
// produces. Every device name gage ever turns into a path component —
// whether it came from --device or was read back from a vault's
// committed config.toml — must pass this before it touches the
// filesystem.
func Valid(name string) bool {
	if name == "" || len(name) > MaxLength {
		return false
	}
	for _, r := range name {
		if !allowed(r) {
			return false
		}
	}
	return true
}
