package gage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/denmark/gage/internal/gage/atomicfile"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/recipients"
)

// entriesDirName is the vault-relative directory every entry file lives
// under — see "On-disk layout".
const entriesDirName = "entries"

// entryFileExt is the suffix on every entry file. Filenames are
// <uuid>.age; nothing else lives in entries/ by construction.
const entryFileExt = ".age"

// ErrEntryNotFound is ReadEntry finding no file at entries/<id>.age.
var ErrEntryNotFound = errors.New("gage: no entry with that id in this vault")

// Timestamp is created/updated's wire representation: RFC 3339, UTC,
// truncated to second precision — the design doc's example
// (2026-01-14T10:32:00Z) carries no fractional seconds, and one wouldn't
// survive a human editing the file in $EDITOR anyway (M5's edit flow). It
// embeds time.Time so every normal time operation still works on it
// directly.
type Timestamp struct {
	time.Time
}

// NewTimestamp normalizes t to the wire's canonical form: UTC, truncated
// to second precision. Every Timestamp gage itself produces — at insert,
// at edit — goes through this rather than a bare Timestamp{t}, so
// "created"/"updated" are always stamped consistently regardless of the
// precision or location of whatever clock produced t.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{t.UTC().Truncate(time.Second)}
}

// MarshalYAML hands back the underlying time.Time rather than a
// formatted string, so yaml.v3 encodes it through its native !!timestamp
// path (unquoted, exactly like the design doc's example) instead of the
// generic string path — which would quote it, since an unquoted
// "2026-01-14T10:32:00Z" reads back ambiguously as a timestamp rather
// than a string. NewTimestamp has already truncated to second precision,
// so no fractional component ever reaches the encoder.
func (t Timestamp) MarshalYAML() (any, error) {
	return t.Time, nil
}

// UnmarshalYAML parses an RFC 3339 timestamp and normalizes it exactly
// the way NewTimestamp does, so a value that round-trips through
// Marshal/Unmarshal compares equal to the one that produced it.
func (t *Timestamp) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("gage: decoding timestamp: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("gage: parsing timestamp %q: %w", s, err)
	}
	*t = NewTimestamp(parsed)
	return nil
}

// Entry is one vault secret, decrypted: the fixed metadata fields plus
// the actual payload. See "Entry format" in the design doc. Field
// declaration order here is the marshaled YAML's field order —
// deliberately matching the design doc's example, since the file is
// shown to a human in M5's $EDITOR flow.
type Entry struct {
	// Title is what ls/search match against and display — required, and
	// the closest thing to a "name" an entry has, though it only exists
	// post-decrypt.
	Title string `yaml:"title"`
	// Description is optional free text, also searchable. Omitted from
	// the wire entirely when empty, never `description: null` or an
	// explicit empty string.
	Description string `yaml:"description,omitempty"`
	// Created is set once, at insert time, and never touched again.
	Created Timestamp `yaml:"created"`
	// Updated and UpdatedBy are rewritten on every edit. UpdatedBy is the
	// local device's identity name — the same one registered via `gage
	// identity add` — recording which already-known identity made the
	// change; no new concept needed.
	Updated   Timestamp `yaml:"updated"`
	UpdatedBy string    `yaml:"updated_by"`
	// Value holds the entry's primary payload: a password, or the body
	// of an unstructured note.
	Value string `yaml:"value"`
	// Fields holds structured key/value metadata (`--field NAME` on
	// show/generate extracts from here). Omitted from the wire entirely
	// when empty, for the same reason as Description.
	Fields map[string]string `yaml:"fields,omitempty"`

	// Extra carries any YAML key this build doesn't recognize. A future
	// gage may add a field; an older binary reading that entry preserves
	// it here rather than silently dropping it on the next `edit`. See
	// the M3 plan's decision on unknown-field preservation.
	Extra map[string]any `yaml:",inline"`
}

// MarshalEntry renders e as the YAML shown in "Entry format".
func MarshalEntry(e Entry) ([]byte, error) {
	data, err := yaml.Marshal(e)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encoding entry: %w", err))
	}
	return data, nil
}

// UnmarshalEntry parses the YAML MarshalEntry produces — or a
// human-edited variant of it, per M5's $EDITOR flow — back into an
// Entry. Keys this build doesn't recognize land in Extra rather than
// causing an error or being silently dropped.
func UnmarshalEntry(data []byte) (Entry, error) {
	var e Entry
	if err := yaml.Unmarshal(data, &e); err != nil {
		return Entry{}, exitcode.Wrap(exitcode.Conflict, fmt.Errorf("gage: parsing entry: %w", err))
	}
	return e, nil
}

// NewEntryID generates a fresh, opaque UUIDv4 entry filename stem — see
// "On-disk layout" for why filenames carry no meaning of their own.
func NewEntryID() uuid.UUID {
	return uuid.New()
}

// entriesDir returns <vault>/entries.
func (v *Vault) entriesDir() string {
	return filepath.Join(v.Path, entriesDirName)
}

// entryPath returns <vault>/entries/<id>.age.
func (v *Vault) entryPath(id uuid.UUID) string {
	return filepath.Join(v.entriesDir(), id.String()+entryFileExt)
}

// WriteEntry encrypts e and writes it to entries/<id>.age, atomically, at
// 0600 — against every recipient currently listed in the vault's
// .age-recipients (see "On-disk layout"). id is the caller's to choose: a
// fresh NewEntryID() for an insert, or an existing entry's own id to
// rewrite it in place (an edit, or M9's --reencrypt).
func (v *Vault) WriteEntry(id uuid.UUID, e Entry) error {
	plaintext, err := MarshalEntry(e)
	if err != nil {
		return err
	}

	to, err := v.encryptRecipients()
	if err != nil {
		return err
	}

	ciphertext, err := Encrypt(plaintext, to...)
	if err != nil {
		return err
	}

	dir := v.entriesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: creating %s: %w", dir, err))
	}
	// MkdirAll leaves an already-existing directory's mode alone, so an
	// entries/ directory created before this rule tightened (or widened
	// by umask) is corrected here rather than trusted.
	// #nosec G302 -- 0700 is the tightest mode a directory can have and
	// still be usable.
	if err := os.Chmod(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: setting permissions on %s: %w", dir, err))
	}

	if err := atomicfile.WriteFile(v.entryPath(id), ciphertext, 0o600); err != nil {
		return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: writing entry %s: %w", id, err))
	}
	return nil
}

// ReadEntry reads and decrypts entries/<id>.age with ident's private key
// and unmarshals the result. Ciphertext that ident isn't a recipient of
// fails with M2's ErrNotARecipient, unwrapped rather than swallowed, so
// callers can tell "wrong key" apart from "damaged file" and "no such
// entry".
func (v *Vault) ReadEntry(id uuid.UUID, ident *Identity) (Entry, error) {
	path := v.entryPath(id)
	// #nosec G304 -- path is built from entriesDir() and a uuid.UUID's
	// own String(), not attacker input.
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, exitcode.Wrap(exitcode.NotFound, fmt.Errorf("%w: %s", ErrEntryNotFound, id))
		}
		return Entry{}, exitcode.Wrap(exitcode.Internal, err)
	}

	plaintext, err := Decrypt(ciphertext, ident)
	if err != nil {
		return Entry{}, err
	}
	return UnmarshalEntry(plaintext)
}

// EntryIDs lists the UUIDs of every entry file in the vault's entries/
// directory, sorted for stable output. It reads only filenames — no
// decryption, no ciphertext access — so it needs no Identity; a vault
// with no entries/ directory yet reports no entries rather than an
// error. See M4's ls and M7's index build.
func (v *Vault) EntryIDs() ([]uuid.UUID, error) {
	dirEntries, err := os.ReadDir(v.entriesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}

	ids := make([]uuid.UUID, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		stem, ok := strings.CutSuffix(name, entryFileExt)
		if !ok {
			continue
		}
		id, err := uuid.Parse(stem)
		if err != nil {
			// Not one of ours — skip rather than fail the whole listing
			// over a stray file that doesn't belong here.
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, nil
}

// encryptRecipients reads and parses the vault's .age-recipients — every
// device that can read a freshly written entry. A plugin recipient
// (age1yubikey1...) is legal there but not encryptable by this build; see
// ParseRecipient.
func (v *Vault) encryptRecipients() ([]Recipient, error) {
	keys, err := recipients.Read(filepath.Join(v.Path, ".age-recipients"))
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: reading .age-recipients: %w", err))
	}
	out := make([]Recipient, len(keys))
	for i, k := range keys {
		r, err := ParseRecipient(k)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}
