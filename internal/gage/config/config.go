// Package config reads and writes gage's global $GAGE_CONFIG/config.toml —
// the file tracking known vaults, this device's per-vault identity/method,
// and shell preferences. See the design doc's "Global config" section.
package config

import (
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"

	"github.com/denmark/gage/internal/gage/atomicfile"
)

// GitMeta is the git-type-specific metadata for a vault entry, namespaced
// under its own table so a future vault type only ever adds a new nested
// table rather than touching this shape.
type GitMeta struct {
	Origin string `toml:"origin,omitempty"`
}

// VaultEntry is one vault's row in the global config: what this device
// calls it, how this device unlocks it, and (for the git type) its remote.
type VaultEntry struct {
	Path string `toml:"path"`
	// ID is a copy of the vault's own committed [vault].id, recorded
	// here so this machine can find the vault's identities directory,
	// trust cache and lock file without reading the vault at all — which
	// is what `vault remove` needs once that vault's files have moved or
	// gone, the situation A20 exists to make safe.
	ID     string `toml:"id,omitempty"`
	Type   string `toml:"type"`
	Device string `toml:"device"`
	// Pubkey is this device's own public key for that vault, recorded
	// when the identity is created (`init`, `identity add`, and
	// enrollment) — the one moment gage holds the key without needing an
	// unlock to reach it.
	//
	// It exists so that "is the key I hold locally still a recipient
	// here?" is answered by comparing *keys* rather than device names: a
	// name is a label, and a label is not proof of which key it refers
	// to. See Q-ORPHAN-BY-NAME, where that confusion gates an
	// irreversible deletion. It is public and already committed inside
	// the vault, so a local plaintext copy discloses nothing new.
	Pubkey string  `toml:"pubkey,omitempty"`
	Method string  `toml:"method"`
	Git    GitMeta `toml:"git,omitempty"`
}

// Shell holds this device's session-mode preferences.
//
// ClipboardTimeout is here rather than in a table of its own for the
// same reason the rest of these are: it is CLI-terminal policy a GUI
// frontend would not share, alongside the prompt template, the idle
// re-lock and the history file.
type Shell struct {
	Prompt           string `toml:"prompt,omitempty"`
	IdleTimeout      string `toml:"idle_timeout,omitempty"`
	HistoryFile      string `toml:"history_file,omitempty"`
	ClipboardTimeout string `toml:"clipboard_timeout,omitempty"`
}

// Global is the full shape of $GAGE_CONFIG/config.toml.
type Global struct {
	Current string                `toml:"current,omitempty"`
	Vaults  map[string]VaultEntry `toml:"vaults,omitempty"`
	Shell   Shell                 `toml:"shell,omitempty"`
}

// Read parses a global config file at path.
func Read(path string) (Global, error) {
	// #nosec G304 -- path is the caller-chosen global config location
	// (resolved from $GAGE_CONFIG by xdgpaths), not attacker-controlled
	// input. The place this rule genuinely bites is a path built from a
	// vault's committed metadata — a device name out of .gage/config.toml,
	// which any git-writer can edit — and that is validated against the
	// character allowlist before any path is constructed from it. See
	// "Device names are validated before they're ever used as a path".
	data, err := os.ReadFile(path)
	if err != nil {
		return Global{}, err
	}
	var g Global
	if err := toml.Unmarshal(data, &g); err != nil {
		return Global{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	return g, nil
}

// Write atomically writes g to path (temp file + fsync + rename — see
// atomicfile), so an interrupted write never leaves a truncated config
// behind.
func Write(path string, g Global) error {
	data, err := toml.Marshal(g)
	if err != nil {
		return fmt.Errorf("config: encoding %s: %w", path, err)
	}
	if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: writing %s: %w", path, err)
	}
	return nil
}
