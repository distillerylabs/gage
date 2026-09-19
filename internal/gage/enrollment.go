package gage

// Device enrollment, library side: the pending directory, the filename
// scheme, listing, resolution and pruning. Sealing and opening live in
// enrollseal.go; the code itself lives in enrollcode.go.
//
// Everything here is built from filenames alone — no git history walk, no
// unlock, no code — which is the property D-ENROLL-EXPIRY-IN-NAME exists
// to buy. See "Expiry, revocation, and pruning".

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

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// DefaultEnrollmentTTL is how long a request lives when nobody says
// otherwise: long enough to cover a request made in one timezone and
// approved the next morning in another, short enough that blobs do not
// sit in the tree for weeks. See D-ENROLL-TTL.
const DefaultEnrollmentTTL = 24 * time.Hour

// MaxEnrollmentTTL is the hard ceiling on a request's lifetime, and it
// does two jobs rather than one. It bounds --ttl at creation, and it
// bounds the expiry gage will *honour* on the way back in: a claim more
// than this far beyond now — in a filename or under the seal — is treated
// as already expired.
//
// That second job is what keeps the ceiling load-bearing now that pruning
// no longer depends on commit dates. A legitimate request is created with
// an expiry at most now+7d, so at any later moment its expiry is at most
// 7d in the future; anything claiming more was forged or written by a
// badly-skewed clock, and without the clamp a hostile git writer could
// park <uuid>-99999999999.age in the tree permanently. Only clock skew
// beyond roughly six days trips it, which is well outside what a working
// git remote tolerates anyway.
const MaxEnrollmentTTL = 7 * 24 * time.Hour

// maxEnrollmentAttempts bounds how many requests one code-trying run will
// attempt, which is a bound on *work* rather than on how many devices may
// enrol: the count comes from a directory any git writer can fill, and
// each attempt is a KDF run. See D-ENROLL-SEAL-COST.
//
// Thirty-two is far above anything legitimate — real transcripts show
// two, and requests expire in 24h. Exceeding it is recoverable in place
// by naming an ID, which narrows the scope to one request from the
// filename alone.
const maxEnrollmentAttempts = 32

// maxPendingRequestBytes bounds how large a file in pending/ gage will
// read, and how large a payload it will hold after decrypting one.
//
// D-ENROLL-SEAL-COST bounds the count of requests and the work factor
// each claims; neither bounds how big a file a git writer puts in the
// directory, and 16 KiB is roughly fifty times the largest request gage
// can produce. It is applied twice because age's plaintext is not bounded
// by its ciphertext's, and the party who can produce a large payload is a
// code-holder — trusted enough to be granted access, not trusted enough
// to be handed an unbounded allocation.
const maxPendingRequestBytes = 16 << 10

// pendingDirName is the vault-relative directory sealed requests live in.
//
// It is created lazily by the first enroll, and its absence is the normal
// state rather than an error: git tracks no empty directories, so a vault
// that has never had a request simply does not have it — including every
// vault that predates this feature.
const pendingDirName = ".gage/pending"

// pendingFileExt is the extension every sealed request carries.
const pendingFileExt = ".age"

// The typed errors enrollment returns. Each is wrapped in an
// exitcode-carrying error where it is returned, per the TDD's "Library
// surface" exit-code table, so cmd/gage gets the category and the status
// from one value.
var (
	// ErrEnrollmentExpired is a request whose authenticated expiry has
	// passed — or is further beyond now than gage would ever have
	// written, which is the same claim from the other direction and
	// deliberately not a separate error.
	ErrEnrollmentExpired = errors.New("gage: this enrollment request has expired")

	// ErrEnrollmentCodeWrong covers both ways a code fails to open
	// anything: one that is well-formed but opens nothing, and one
	// rejected up front on length or alphabet (D-ENROLL-CODE-FORMAT)
	// without a decryption being attempted at all. They are deliberately
	// the *same* error despite being distinguishable, because the caller
	// does the same thing with both — cmd/gage's retry loop is keyed on
	// this value, and a typo is precisely the case that loop exists for.
	// Splitting them would exit the command on the likeliest mistake
	// while retrying the rarer one.
	//
	// The validation still happens before any decryption; what that buys
	// is cost, not a different answer.
	ErrEnrollmentCodeWrong = errors.New("gage: no pending request opened with that code")

	// ErrEnrollmentIDMismatch is a request whose sealed request_id
	// disagrees with its filename's UUID portion — a blob renamed onto a
	// different slot, or replayed into a fresh one. The epoch portion is
	// an unauthenticated hint and is deliberately not part of the check.
	ErrEnrollmentIDMismatch = errors.New("gage: this request's sealed id does not match its filename")

	// ErrEnrollmentMalformedRequest is a code opening a request whose
	// sealed payload is not something gage could have written — a device
	// name outside the filesystem-safe allowlist, a pubkey that is not an
	// age recipient, an unknown method, an id that is not a UUID.
	//
	// Deliberately *not* ErrEnrollmentCodeWrong: the code worked.
	// Reporting it as a wrong code would put cmd/gage's retry loop into a
	// re-prompt no correct code can ever satisfy. See "The sealed payload
	// is untrusted input".
	ErrEnrollmentMalformedRequest = errors.New("gage: this enrollment request's sealed contents are malformed")

	// ErrEnrollmentWrongVault is a code opening a well-formed request
	// sealed for a *different* vault.
	//
	// Distinct from both its neighbours on purpose. Not
	// ErrEnrollmentCodeWrong: the code worked, and a retry loop would
	// otherwise re-prompt for a code that cannot be right here however
	// carefully it is typed. Not ErrEnrollmentMalformedRequest either:
	// the payload is well-formed and gage produced it, so reporting
	// corruption would send a human looking for damage that isn't there.
	// The message names the vault the request is for.
	ErrEnrollmentWrongVault = errors.New("gage: this enrollment request is for a different vault")

	// ErrEnrollmentNoSuchRequest is an ID naming no pending request. Its
	// sibling — an ID matching several — is not an error but a candidate
	// list; see AmbiguousRequestError.
	ErrEnrollmentNoSuchRequest = errors.New("gage: no pending enrollment request with that id")

	// ErrEnrollmentTooManyPending is a run with no ID that would have to
	// try more than maxEnrollmentAttempts live requests. Refused before
	// any decryption, and recoverable in place by naming an ID — which
	// resolves from the filename alone and costs one run per code however
	// full the directory is. See D-ENROLL-SEAL-COST.
	ErrEnrollmentTooManyPending = errors.New("gage: too many pending enrollment requests to try a code against them all; approve or deny by id")

	// ErrEnrollmentClockSkew is a request whose sealed expiry was already
	// in the past when it was created: it was dead on arrival, which is a
	// wrong clock rather than a stale request. Distinct from
	// ErrEnrollmentExpired because the fixes share nothing — fix that
	// machine's clock and enroll again, versus ask for a fresh request.
	ErrEnrollmentClockSkew = errors.New("gage: this enrollment request expired before it was created, so the requesting device's clock is wrong")
)

// EnrollmentRequest is what a joining device produced. Code is the
// generated out-of-band secret — the one field cmd/gage must display and
// the library must never persist.
//
// Published is false in exactly one case: this device's recorded pubkey
// is already a recipient of the vault, so there was nothing to ask for.
// Every other field is then zero, Code included. It is a success, not an
// error. Nothing in this milestone sets it — E3's Enroll owns that case —
// but the field is part of the type's published shape.
type EnrollmentRequest struct {
	ID        string
	Device    string
	Pubkey    string
	Method    string
	Created   time.Time
	Expires   time.Time
	Code      string
	Published bool
}

// PendingRequest is one unopened request, built entirely from its
// filename — no git history walk, no unlock, no code.
type PendingRequest struct {
	ID   string
	Path string
	// Expires is unauthenticated; it is parsed from the filename's epoch.
	// Display and pruning only — approval reads the sealed copy. See
	// D-ENROLL-EXPIRY-IN-NAME.
	Expires time.Time
}

// OpenedRequest is a PendingRequest whose seal a code has opened. Every
// field here is authenticated — and validated: authenticated says the
// bytes are unaltered, not that they are well-formed, so device, pubkey,
// method, request_id, vault_id and the two timestamps are all checked
// before one of these is returned.
//
// There is deliberately no VaultID field. The sealed value is compared
// against this vault's own id and the request refused on a mismatch, so
// every OpenedRequest that exists is one for this vault — a field would
// be a constant the caller could only re-check.
type OpenedRequest struct {
	ID      string
	Device  string
	Pubkey  string
	Method  string
	Created time.Time
	Expires time.Time
}

// AmbiguousRequestError carries the requests an ID matched when it
// matched more than one. It is a value cmd/gage renders, not printed
// text — the same shape as M5's CandidateList, and for the same reason:
// deny deletes, so a resolver that guessed would delete a request the
// human did not name.
type AmbiguousRequestError struct {
	ID      string
	Matches []PendingRequest
}

func (e *AmbiguousRequestError) Error() string {
	return fmt.Sprintf("gage: %q matches %d pending enrollment requests", e.ID, len(e.Matches))
}

// pendingDir returns <vault>/.gage/pending.
func (v *Vault) pendingDir() string {
	return filepath.Join(v.Path, filepath.FromSlash(pendingDirName))
}

// pendingFileName renders a request's filename:
// <request-id>-<expires-epoch>.age.
//
// The device name is deliberately absent. Naming the file after the
// device would hand every reader of the vault a free "someone is setting
// up a new machine" signal before anyone approved anything; the name
// becomes public on approval and there is no reason to leak it before, or
// at all if the request is denied or expires.
func pendingFileName(id string, expires time.Time) string {
	return fmt.Sprintf("%s-%d%s", id, expires.UTC().Unix(), pendingFileExt)
}

// parsePendingFileName splits a pending filename back into its two
// halves, reporting false for anything gage did not write.
//
// The halves are validated independently, and neither is trusted because
// the other parsed: a directory in a git repository is a surface anyone
// with write access can put files into, so an unparseable name is skipped
// (and, in pruning, left strictly alone) rather than guessed at.
//
// The epoch is found at the last hyphen, which is unambiguous because a
// canonical UUID's own hyphens all precede it and the epoch itself is
// digits only — a signed or padded spelling is refused rather than
// normalized.
func parsePendingFileName(name string) (id string, expires time.Time, ok bool) {
	stem, found := strings.CutSuffix(name, pendingFileExt)
	if !found {
		return "", time.Time{}, false
	}
	cut := strings.LastIndexByte(stem, '-')
	if cut < 0 {
		return "", time.Time{}, false
	}
	id, epoch := stem[:cut], stem[cut+1:]
	if !canonicalUUID(id) || !allDigits(epoch) {
		return "", time.Time{}, false
	}
	secs, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil {
		return "", time.Time{}, false
	}
	return id, time.Unix(secs, 0).UTC(), true
}

// canonicalUUID reports whether s is a UUID in the one spelling gage
// writes: lowercase 8-4-4-4-12.
//
// Stricter than uuid.Parse, which also accepts a urn:uuid: prefix, a
// braced form and an undashed 32-hex string. Those denote the same UUID
// but spell it differently, and a request with several legal spellings of
// its id is a request the filename comparison could be walked around.
func canonicalUUID(s string) bool {
	parsed, err := uuid.Parse(s)
	if err != nil {
		return false
	}
	return parsed != uuid.Nil && parsed.String() == s
}

// allDigits reports whether s is one or more ASCII digits and nothing
// else — no sign, no padding, no separators.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// enrollmentExpiryLive reports whether an expiry gage has been handed is
// one it will honour at now: in the future, and not further into it than
// gage could ever have written.
//
// Both halves are the same rule from opposite ends, which is why they sit
// in one function rather than being applied ad hoc at each call site. It
// is used against the filename's unauthenticated epoch (listing, pruning)
// and against the sealed copy (opening), and it must agree in both places
// or a request would list as live and refuse to open, or the reverse.
func enrollmentExpiryLive(expires, now time.Time) bool {
	return expires.After(now) && !expires.After(now.Add(MaxEnrollmentTTL))
}

// PendingEnrollments returns the *live* requests in this vault's
// pending/: one whose filename epoch is in the past, or more than the
// ceiling beyond now, is filtered out rather than returned.
//
// Filtered, not deleted. Listing is a read — it takes no lock and writes
// nothing — so removing the file is pruning's job and rides the next
// write. That split is load-bearing in two places: `recipient pending`
// must not present an expired request as pending, and
// D-ENROLL-SEAL-COST's 32-request bound counts what this returns, so
// ordinary neglect can never look like an attack whether or not a write
// has come along to prune yet.
//
// A vault with no pending/ directory reports zero requests rather than an
// error — the normal state, not an edge case. Strays and oversized files
// are skipped, the same posture ListIdentities takes. Nothing here reads
// git history, needs an unlock, or opens a single file.
func (v *Vault) PendingEnrollments() ([]PendingRequest, error) {
	dirEntries, err := os.ReadDir(v.pendingDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}

	now := time.Now()
	out := make([]PendingRequest, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		id, expires, ok := parsePendingFileName(de.Name())
		if !ok {
			continue
		}
		// The size bound, taken from the directory entry: an oversized
		// file is skipped before any read, any KDF run and any
		// allocation. Skipped rather than deleted — it is by definition
		// not something gage wrote, which is the same reason the name
		// parser leaves strays alone.
		info, err := de.Info()
		if err != nil || info.Size() > maxPendingRequestBytes {
			continue
		}
		if !enrollmentExpiryLive(expires, now) {
			continue
		}
		out = append(out, PendingRequest{
			ID:      id,
			Path:    filepath.Join(v.pendingDir(), de.Name()),
			Expires: expires,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ResolveEnrollment turns a human-typed id into one pending request,
// applying the base design's resolution order over the UUID portion of
// the filename only: exact, then substring, then a candidate list.
//
// The epoch is parsed off before any matching happens and is not part of
// the searchable text. That is not fussiness — a pending filename carries
// ten digits of epoch, all of them legal hex, so a short id like "1788"
// could match the *expiry* of one request and the *id* of another, and a
// naive substring search over whole filenames would resolve to whichever
// it hit first. Deny deletes, so that bug deletes the wrong request.
//
// Ambiguity is a *AmbiguousRequestError carrying every match, never a
// guess; no match is ErrEnrollmentNoSuchRequest. Both approve and deny go
// through this, so there is one resolution rule rather than one per verb.
func (v *Vault) ResolveEnrollment(id string) (PendingRequest, error) {
	pending, err := v.PendingEnrollments()
	if err != nil {
		return PendingRequest{}, err
	}

	if match, matches, done := resolveRequestStage(id, matchExactRequestID(id, pending)); done {
		return match, matches
	}
	if match, matches, done := resolveRequestStage(id, matchSubstringRequestID(id, pending)); done {
		return match, matches
	}
	return PendingRequest{}, exitcode.Wrap(exitcode.NotFound,
		fmt.Errorf("%w: %q", ErrEnrollmentNoSuchRequest, id))
}

// resolveRequestStage turns one resolution stage's match set into either
// "not this stage, keep going" (done == false) or a final outcome: the
// sole match, or an ambiguity over every match at this stage.
func resolveRequestStage(id string, matches []PendingRequest) (PendingRequest, error, bool) {
	switch len(matches) {
	case 0:
		return PendingRequest{}, nil, false
	case 1:
		return matches[0], nil, true
	default:
		return PendingRequest{}, exitcode.Wrap(exitcode.Ambiguous,
			&AmbiguousRequestError{ID: id, Matches: matches}), true
	}
}

// matchExactRequestID is the first stage: the whole 36 characters,
// accepting any of uuid.Parse's spellings on the typed side so that a
// braced or uppercased id still resolves exactly rather than falling
// through to a substring search that would not match it.
func matchExactRequestID(id string, pending []PendingRequest) []PendingRequest {
	typed, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	var matches []PendingRequest
	for _, r := range pending {
		if r.ID == typed.String() {
			matches = append(matches, r)
		}
	}
	return matches
}

// matchSubstringRequestID is the second stage: every request whose id
// contains the typed text, hyphens and case ignored on both sides — a
// superset of "prefix", so the short ids a listing displays keep working.
func matchSubstringRequestID(id string, pending []PendingRequest) []PendingRequest {
	needle := strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if needle == "" {
		return nil
	}
	var matches []PendingRequest
	for _, r := range pending {
		haystack := strings.ReplaceAll(r.ID, "-", "")
		if strings.Contains(haystack, needle) {
			matches = append(matches, r)
		}
	}
	return matches
}

// prunePendingEnrollments deletes the requests in pending/ that gage will
// never honour again — expired by filename, or claiming an expiry further
// beyond now than gage could have written — and returns the names it
// removed.
//
// It reads the filename's epoch and nothing else: no history walk, no
// unlock, no code. gage prunes opportunistically when it is already
// writing rather than taking the lock to do housekeeping alone, so
// callers run this inside a write they were performing anyway.
//
// A name that does not parse is left strictly alone, and so is an
// oversized file: neither is something gage wrote, and refusing to delete
// a file it cannot account for is the conservative direction — the same
// posture the parser and the listing already take. The filename's epoch
// lying in the other direction (a name claiming expiry on a still-valid
// seal) prunes a live request, which is a denial of service but not a new
// one: anyone who can rename the file can equally delete it.
func (v *Vault) prunePendingEnrollments() ([]string, error) {
	dirEntries, err := os.ReadDir(v.pendingDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, exitcode.Wrap(exitcode.Internal, err)
	}

	now := time.Now()
	var removed []string
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		_, expires, ok := parsePendingFileName(de.Name())
		if !ok {
			continue
		}
		if enrollmentExpiryLive(expires, now) {
			continue
		}
		if err := os.Remove(filepath.Join(v.pendingDir(), de.Name())); err != nil {
			return removed, exitcode.Wrap(exitcode.Internal,
				fmt.Errorf("gage: pruning expired enrollment request %s: %w", de.Name(), err))
		}
		removed = append(removed, de.Name())
	}
	sort.Strings(removed)
	return removed, nil
}
