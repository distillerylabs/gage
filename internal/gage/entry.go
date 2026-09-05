package gage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/denmark/gage/internal/gage/atomicfile"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/recipients"
)

// entriesDirName is the vault-relative directory every entry file lives
// under — see "On-disk layout".
const entriesDirName = "entries"

// entryFileExt is the suffix on every entry file. Filenames are
// <uuid>.age; nothing else lives in entries/ by construction.
const entryFileExt = ".age"

// ErrEntryNotFound is ReadEntry finding no file at entries/<id>.age, or a
// title query matching nothing.
var ErrEntryNotFound = errors.New("gage: no entry with that id in this vault")

// ErrDuplicateTitle is Insert refusing to create a second entry with a
// title that already matches an existing one, without force.
var ErrDuplicateTitle = errors.New("gage: an entry with this title already exists")

// ErrAmbiguousQuery is Resolve finding more than one entry at whichever
// resolution stage first produced a match — possible once a duplicate
// title has been forced into existence, or whenever a query's prefix or
// substring match isn't unique. Session mode (M6) turns the same
// candidate list into an interactive pick; one-shot mode here has nobody
// to ask, so it fails instead.
var ErrAmbiguousQuery = errors.New("gage: more than one entry matches that query")

// AmbiguousQueryError is Resolve's ambiguous outcome, carrying the
// candidate list as data rather than as printed text — see "Addressing
// entries & the metadata index" and the M5 plan's "the resolver returns a
// candidate list as a *value*; no library code path prints it." A
// one-shot CLI renders List and fails; a session-mode CLI would render it
// as a picker instead. Callers match ErrAmbiguousQuery via errors.Is, or
// this type via errors.As when they need the candidates themselves.
type AmbiguousQueryError struct {
	List CandidateList
}

func (e *AmbiguousQueryError) Error() string {
	return fmt.Sprintf("%s: %q matches %d entries", ErrAmbiguousQuery, e.List.Query, len(e.List.Candidates))
}

func (e *AmbiguousQueryError) Unwrap() error { return ErrAmbiguousQuery }

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

// MarshalYAML keeps a Timestamp correct as a yaml value in its own
// right. MarshalEntry does not go through it — it emits timestamps via
// timestampNode, along with every other scalar in the document — so this
// exists for a caller who marshals a Timestamp or an Entry directly, and
// its output has to agree with timestampNode's. It hands back the
// underlying time.Time so the encoder uses its native !!timestamp path
// (unquoted, as the design doc's example shows) rather than quoting a
// string that would otherwise read back ambiguously as a timestamp.
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
// the actual payload. See "Entry format" in the design doc.
//
// The yaml tags drive decoding only; marshalEntryNodes writes the
// document field by field and is what actually fixes the on-the-wire
// order (the design doc's, since M5 shows this file to a human in
// $EDITOR). The two must stay in agreement — a field added here needs a
// line there, or it will be read back but never written.
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
//
// The document is built as an explicit node tree rather than by handing
// the struct to yaml.Marshal, because the library's own scalar-style
// selection silently corrupts values a password manager must store
// exactly. Handing yaml.v3 a value with a leading newline emits a block
// scalar that reads back one newline short ("\nsecret" -> "secret"), and
// a value containing a tab emits a block scalar the same library then
// refuses to parse — so the entry would encrypt cleanly and never open
// again. goccy/go-yaml, the other library the M3 plan weighed, fixes the
// first and fails the same way on tabs, CRLF and leading tabs, so this is
// a property gage has to own rather than delegate. See entryScalar for
// the style rule and verifyFaithful for the check that backs it up.
func MarshalEntry(e Entry) ([]byte, error) {
	// Stamp the wire's canonical timestamp form here rather than trusting
	// every caller to have gone through NewTimestamp, so second precision
	// is a property of the format instead of a convention.
	e.Created = NewTimestamp(e.Created.Time)
	e.Updated = NewTimestamp(e.Updated.Time)

	data, err := marshalEntryNodes(e, styleReadable)
	if err != nil {
		return nil, err
	}
	if verifyFaithful(data, e) == nil {
		return data, nil
	}

	// The readable styling did not survive its own round trip. Fall back
	// to quoting every string, the one style that round-trips everything
	// tested, and refuse outright if even that is not faithful — a
	// corrupted secret written without complaint is the one outcome this
	// layer must never produce.
	data, err = marshalEntryNodes(e, styleAlwaysQuoted)
	if err != nil {
		return nil, err
	}
	if err := verifyFaithful(data, e); err != nil {
		return nil, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: refusing to write an entry that does not survive its own YAML round trip: %w", err))
	}
	return data, nil
}

// quoting selects how far marshalEntryNodes goes to keep strings intact.
type quoting int

const (
	// styleReadable keeps the file pleasant to edit by hand (M5's $EDITOR
	// flow): block scalars for multi-line values that can survive one,
	// quotes only where they are needed.
	styleReadable quoting = iota
	// styleAlwaysQuoted quotes every string, trading readability for the
	// widest fidelity. Only the fallback path uses it.
	styleAlwaysQuoted
)

// marshalEntryNodes builds the document node by node, in the field order
// "Entry format" fixes, and emits it.
func marshalEntryNodes(e Entry, q quoting) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(key string, value *yaml.Node) {
		doc.Content = append(doc.Content, entryScalar(key, q), value)
	}

	add("title", entryScalar(e.Title, q))
	if e.Description != "" {
		add("description", entryScalar(e.Description, q))
	}
	add("created", timestampNode(e.Created))
	add("updated", timestampNode(e.Updated))
	add("updated_by", entryScalar(e.UpdatedBy, q))
	add("value", entryScalar(e.Value, q))
	if len(e.Fields) > 0 {
		fields := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range sortedKeys(e.Fields) {
			fields.Content = append(fields.Content, entryScalar(k, q), entryScalar(e.Fields[k], q))
		}
		add("fields", fields)
	}
	// Unknown keys are emitted after the known ones. Their original
	// position isn't recoverable — Extra is a map — so sorting them is
	// what makes the output deterministic.
	for _, k := range sortedKeys(e.Extra) {
		add(k, unknownValueNode(e.Extra[k], q))
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: encoding entry: %w", err))
	}
	return data, nil
}

// entryScalar builds one string scalar, choosing the style that both
// round-trips and reads well.
//
// A multi-line value gets a literal block scalar only when it can
// actually survive one: no leading blank line (the emitter drops it), no
// tab or control character (the emitter writes a block it cannot re-read),
// and no line ending in whitespace (block scalars strip it). Everything
// else multi-line is double-quoted, which escapes those characters
// explicitly. Single-line strings are left to the emitter, which quotes
// "null", "true", "123" and friends on its own.
func entryScalar(s string, q quoting) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	switch {
	case q == styleAlwaysQuoted:
		n.Style = yaml.DoubleQuotedStyle
	case blockScalarSafe(s):
		n.Style = yaml.LiteralStyle
	case strings.ContainsAny(s, "\n\r\t"):
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// blockScalarSafe reports whether s can be written as a literal block
// scalar and read back byte for byte.
func blockScalarSafe(s string) bool {
	if !strings.Contains(s, "\n") || strings.HasPrefix(s, "\n") {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		if line != strings.TrimRight(line, " \t") {
			return false
		}
		for _, r := range line {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

// timestampNode emits created/updated as an RFC 3339 scalar. The
// !!timestamp tag is what keeps it unquoted in the output — the tag
// itself stays invisible, since it is the one YAML would infer anyway —
// giving the bare `created: 2026-01-14T10:32:00Z` the design doc shows.
func timestampNode(t Timestamp) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: t.Format(time.RFC3339)}
}

// unknownValueNode renders one value from Extra. It handles the closed
// set of Go types a generic YAML decode produces, so a future gage's
// fields get the same styling care as gage's own; anything else — only
// reachable if a caller populated Extra by hand with an exotic type —
// falls back to the library's own encoding.
func unknownValueNode(v any, q quoting) *yaml.Node {
	switch t := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case string:
		return entryScalar(t, q)
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(t)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(t, 10)}
	case uint64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(t, 10)}
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(t, 'g', -1, 64)}
	case []any:
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range t {
			seq.Content = append(seq.Content, unknownValueNode(item, q))
		}
		return seq
	case map[string]any:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range sortedKeys(t) {
			m.Content = append(m.Content, entryScalar(k, q), unknownValueNode(t[k], q))
		}
		return m
	default:
		var n yaml.Node
		if err := n.Encode(v); err != nil {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(v), Style: yaml.DoubleQuotedStyle}
		}
		return &n
	}
}

// verifyFaithful re-reads freshly marshaled bytes and reports whether
// every string gage itself owns came back unchanged. It is the check that
// makes "an entry round-trips byte for byte" a property gage enforces
// rather than one it inherits from a library's style heuristics.
func verifyFaithful(data []byte, want Entry) error {
	got, err := UnmarshalEntry(data)
	if err != nil {
		return fmt.Errorf("the entry did not parse back: %w", err)
	}
	for _, f := range []struct {
		name      string
		got, want string
	}{
		{"title", got.Title, want.Title},
		{"description", got.Description, want.Description},
		{"updated_by", got.UpdatedBy, want.UpdatedBy},
		{"value", got.Value, want.Value},
	} {
		if f.got != f.want {
			return fmt.Errorf("%s changed: wrote %q, read back %q", f.name, f.want, f.got)
		}
	}
	if len(got.Fields) != len(want.Fields) {
		return fmt.Errorf("fields changed: wrote %d keys, read back %d", len(want.Fields), len(got.Fields))
	}
	for k, v := range want.Fields {
		if got.Fields[k] != v {
			return fmt.Errorf("field %q changed: wrote %q, read back %q", k, v, got.Fields[k])
		}
	}
	return nil
}

// sortedKeys returns m's keys in a deterministic order, so one entry
// always marshals to one byte sequence.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	if v.onDecrypt != nil {
		v.onDecrypt()
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

// Insert creates a brand-new entry: unless force is set, it refuses a
// title that already matches an existing entry; otherwise it assigns a
// fresh id, encrypts and writes the entry under entries/, and commits —
// the whole read-modify-commit sequence under this vault's write lock
// (see withWriteLock). It returns the new entry's id.
func (v *Vault) Insert(e Entry, force bool, ident *Identity) (uuid.UUID, error) {
	var id uuid.UUID
	err := v.withWriteLock(func() error {
		if !force {
			exists, err := v.titleExists(e.Title, ident)
			if err != nil {
				return err
			}
			if exists {
				return exitcode.Wrap(exitcode.Conflict,
					fmt.Errorf("%w: %q", ErrDuplicateTitle, e.Title))
			}
		}

		id = NewEntryID()
		if err := v.WriteEntry(id, e); err != nil {
			return err
		}
		if _, err := gitrepo.CommitAll(v.Path, id.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing entry %s: %w", id, err))
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Remove resolves query (an id or an exact title match — see Resolve) and
// deletes its entry file, then commits the deletion — the whole
// read-modify-commit sequence under this vault's write lock. It returns
// the removed entry's id.
func (v *Vault) Remove(query string, ident *Identity) (uuid.UUID, error) {
	var id uuid.UUID
	err := v.withWriteLock(func() error {
		var err error
		id, _, err = v.Resolve(query, ident)
		if err != nil {
			return err
		}
		if err := os.Remove(v.entryPath(id)); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: removing entry %s: %w", id, err))
		}
		if _, err := gitrepo.CommitAll(v.Path, id.String()); err != nil {
			return exitcode.Wrap(exitcode.Internal, fmt.Errorf("gage: committing removal of %s: %w", id, err))
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Resolve addresses one entry by query, in the order "Addressing entries
// & the metadata index" fixes: exact title match → substring match on
// title → exact UUID → substring match on a UUID's canonical string
// form → ambiguous. Every `show`/`cat`/`edit`/`rename`/`rm` command goes
// through this one method — see the M5 plan's "no command should be left
// on M4's exact-match path."
//
// A stage that produces zero matches falls through to the next; a stage
// that produces more than one stops there and reports *that* stage's
// matches as ambiguous, never spilling into a later stage — an exact
// title match always wins over a substring match of a different entry,
// even when the substring stage would itself have been unique.
//
// Title is checked before UUID on purpose, not merely by convention: see
// the M5 plan's "Decisions made" for the bug this ordering fixes — an
// earlier version of this resolver tried UUID matches first, which let a
// query naming one entry's *exact, literal title* silently resolve to a
// *different* entry instead, whenever the query also happened to be a
// unique UUID substring elsewhere in the vault. Checking every title
// stage first closes that hole structurally: by the time UUID matching
// ever runs, no entry's title matched the query at all, so a UUID hit can
// never pre-empt a title hit.
//
// It never takes the write lock: resolving is a read. There's no
// metadata index at the Vault level — Vault stays a stateless,
// identity-agnostic operator over ciphertext, so it decrypts every
// entry on every call, in both invocation modes. Session's index (M7)
// is what caches this for a session, by calling resolveTitleAndUUID
// (the same stage logic below) against its own cached titles instead of
// a freshly decrypted map, and only decrypting the one entry that wins.
func (v *Vault) Resolve(query string, ident *Identity) (uuid.UUID, Entry, error) {
	entries, err := v.decryptAll(ident)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}
	titles := make(map[uuid.UUID]string, len(entries))
	for id, e := range entries {
		titles[id] = e.Title
	}

	id, err := resolveTitleAndUUID(query, titles)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}
	return id, entries[id], nil
}

// decryptAll decrypts every entry in the vault and returns them keyed by
// id — the one full-decrypt pass behind Resolve and titleExists, and (from
// M7) behind Session's index build and its body-text search. Vault stays
// identity-agnostic and stateless around it: nothing here is cached
// beyond the single call.
func (v *Vault) decryptAll(ident *Identity) (map[uuid.UUID]Entry, error) {
	ids, err := v.EntryIDs()
	if err != nil {
		return nil, err
	}
	entries := make(map[uuid.UUID]Entry, len(ids))
	for _, id := range ids {
		e, err := v.ReadEntry(id, ident)
		if err != nil {
			return nil, err
		}
		entries[id] = e
	}
	return entries, nil
}

// resolveTitleAndUUID runs the four-stage resolution algorithm — exact
// title → substring title → exact UUID → substring UUID → not found —
// against titles alone, so it can run identically over a freshly
// decrypted map (Vault.Resolve) or a session's cached index (M7's
// Session.Resolve), and the two can never silently drift apart.
//
// A stage that produces zero matches falls through to the next; a stage
// that produces more than one stops there and reports *that* stage's
// matches as ambiguous, never spilling into a later stage — an exact
// title match always wins over a substring match of a different entry,
// even when the substring stage would itself have been unique.
//
// Title is checked before UUID on purpose, not merely by convention: see
// the M5 plan's "Decisions made" for the bug this ordering fixes — an
// earlier version of this resolver tried UUID matches first, which let a
// query naming one entry's *exact, literal title* silently resolve to a
// *different* entry instead, whenever the query also happened to be a
// unique UUID substring elsewhere in the vault. Checking every title
// stage first closes that hole structurally: by the time UUID matching
// ever runs, no entry's title matched the query at all, so a UUID hit can
// never pre-empt a title hit.
func resolveTitleAndUUID(query string, titles map[uuid.UUID]string) (uuid.UUID, error) {
	if id, err, done := resolveStage(query, matchExactTitle(query, titles), titles); done {
		return id, err
	}
	if id, err, done := resolveStage(query, matchSubstringTitle(query, titles), titles); done {
		return id, err
	}
	if id, err, done := resolveStage(query, matchExactUUID(query, titles), titles); done {
		return id, err
	}
	if id, err, done := resolveStage(query, matchSubstringUUID(query, titles), titles); done {
		return id, err
	}

	return uuid.Nil, exitcode.Wrap(exitcode.NotFound,
		fmt.Errorf("%w: %q", ErrEntryNotFound, query))
}

// matchExactTitle is the first stage: every entry whose title is
// byte-for-byte equal to query.
func matchExactTitle(query string, titles map[uuid.UUID]string) []uuid.UUID {
	var matches []uuid.UUID
	for id, title := range titles {
		if title == query {
			matches = append(matches, id)
		}
	}
	return matches
}

// matchSubstringTitle is the second stage: every entry whose title
// contains query, case-insensitively. Title-only — description and body
// are `search`'s job, not addressing's; see the M5 plan's "Decisions
// made."
func matchSubstringTitle(query string, titles map[uuid.UUID]string) []uuid.UUID {
	lowerQuery := strings.ToLower(query)
	var matches []uuid.UUID
	for id, title := range titles {
		if strings.Contains(strings.ToLower(title), lowerQuery) {
			matches = append(matches, id)
		}
	}
	return matches
}

// matchExactUUID is the third stage: query parsed as a UUID (accepting
// any of uuid.Parse's accepted spellings — hyphenated, hyphen-less,
// braced, urn:uuid: prefixed) against every entry's id, exactly. A query
// that doesn't even parse as a UUID matches nothing here rather than
// erroring — resolution's job is to keep trying stages, not to reject a
// query early because one particular stage can't use it.
func matchExactUUID(query string, titles map[uuid.UUID]string) []uuid.UUID {
	qid, err := uuid.Parse(query)
	if err != nil {
		return nil
	}
	if _, ok := titles[qid]; !ok {
		return nil
	}
	return []uuid.UUID{qid}
}

// matchSubstringUUID is the fourth and last stage: every entry whose
// canonical UUID string contains query as a substring, hyphens and case
// ignored on both sides — a superset of "prefix," so `ls`'s short-id
// convention keeps working. A query with no hex-and-hyphen characters at
// all simply can't be a substring of any UUID's canonical form (which is
// exactly those characters), so this stage naturally matches nothing for
// an ordinary word without needing a separate "does this look like a
// UUID" guard the way an earlier version of this stage did.
func matchSubstringUUID(query string, titles map[uuid.UUID]string) []uuid.UUID {
	strippedQuery := strings.ToLower(strings.ReplaceAll(query, "-", ""))
	var matches []uuid.UUID
	for id := range titles {
		strippedID := strings.ToLower(strings.ReplaceAll(id.String(), "-", ""))
		if strings.Contains(strippedID, strippedQuery) {
			matches = append(matches, id)
		}
	}
	return matches
}

// resolveStage turns one resolution stage's match set into either "not
// this stage, keep going" (done == false, zero matches) or a final
// outcome (done == true): the sole match, or an AmbiguousQueryError over
// every match at this stage.
func resolveStage(query string, matches []uuid.UUID, titles map[uuid.UUID]string) (id uuid.UUID, err error, done bool) {
	switch len(matches) {
	case 0:
		return uuid.Nil, nil, false
	case 1:
		return matches[0], nil, true
	default:
		return uuid.Nil, ambiguousError(query, matches, titles), true
	}
}

// ambiguousError builds an AmbiguousQueryError over ids, sorted by title
// then id for a deterministic, human-sensible candidate list.
func ambiguousError(query string, ids []uuid.UUID, titles map[uuid.UUID]string) error {
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool {
		ti, tj := titles[sorted[i]], titles[sorted[j]]
		if ti != tj {
			return ti < tj
		}
		return sorted[i].String() < sorted[j].String()
	})
	candidates := make([]Candidate, len(sorted))
	for i, id := range sorted {
		candidates[i] = Candidate{ID: id.String(), Title: titles[id]}
	}
	return exitcode.Wrap(exitcode.Ambiguous,
		&AmbiguousQueryError{List: CandidateList{Query: query, Candidates: candidates}})
}

// titleExists reports whether any entry currently has exactly title,
// decrypting every entry to check — there is no Vault-level index (M4
// explicitly decrypts everything every time; M7's cache lives on
// Session, not here). Used by Insert's duplicate-title guard.
func (v *Vault) titleExists(title string, ident *Identity) (bool, error) {
	entries, err := v.decryptAll(ident)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Title == title {
			return true, nil
		}
	}
	return false, nil
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
