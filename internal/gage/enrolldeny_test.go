package gage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
)

// ---------------------------------------------------------------------
// deny: refusing a request grants nothing, so it proves nothing — no
// code, no unlock, no Identity. It is still a write, so it takes the
// lock, prunes what it finds, commits, and warns if the push fails.
// ---------------------------------------------------------------------

// TestDenyRemovesTheRequestAndGrantsNothing.
func TestDenyRemovesTheRequestAndGrantsNothing(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	before, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	head := headHash(t, v)

	// No code and no Identity anywhere in this call, which is the point.
	p := &fakePrompter{}
	if err := v.DenyEnrollment(req.ID, p); err != nil {
		t.Fatalf("DenyEnrollment: %v", err)
	}

	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("pending/ still holds %v after deny", files)
	}
	after, err := v.Recipients()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("deny changed the recipient list from %d to %d entries", len(before), len(after))
	}
	if headHash(t, v) == head {
		t.Fatal("deny left HEAD untouched; the removal was never committed")
	}
}

// TestDenyResolvesAnEightCharacterPrefix — the form the TDD's own
// transcript uses.
func TestDenyResolvesAnEightCharacterPrefix(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	if err := v.DenyEnrollment(req.ID[:8], &fakePrompter{}); err != nil {
		t.Fatalf("DenyEnrollment with an 8-character prefix: %v", err)
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("pending/ still holds %v after deny", files)
	}
}

// TestDenyOnAnAmbiguousIDDeletesNothing. deny deletes, so a resolver
// that guessed would delete a request nobody named.
func TestDenyOnAnAmbiguousIDDeletesNothing(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	enrolled(t, v, remote, "phone-1")
	enrolled(t, v, remote, "phone-2")

	if got := len(pendingFiles(t, v)); got != 2 {
		t.Fatalf("set up %d pending requests, want 2", got)
	}

	// The empty-ish substring every id contains: every hex id matches "".
	// Use a single character present in both instead, found by hand.
	err := v.DenyEnrollment(commonSubstring(t, v), &fakePrompter{})
	var ambiguous *AmbiguousRequestError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("DenyEnrollment on an ambiguous id = %v, want *AmbiguousRequestError", err)
	}
	if len(ambiguous.Matches) != 2 {
		t.Fatalf("ambiguity carried %d matches, want 2", len(ambiguous.Matches))
	}
	if got := exitcode.CodeOf(err); got != exitcode.Ambiguous {
		t.Fatalf("exit code = %v, want Ambiguous", got)
	}
	if got := len(pendingFiles(t, v)); got != 2 {
		t.Fatalf("an ambiguous deny deleted something: %d requests left, want 2", got)
	}
}

// commonSubstring finds a short string every pending request's id
// contains, so an ambiguity can be produced from ids the test did not
// choose.
func commonSubstring(t *testing.T, v *Vault) string {
	t.Helper()
	pending, err := v.PendingEnrollments()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range "0123456789abcdef" {
		hits := 0
		for _, r := range pending {
			if strings.ContainsRune(strings.ReplaceAll(r.ID, "-", ""), c) {
				hits++
			}
		}
		if hits == len(pending) {
			return string(c)
		}
	}
	t.Fatal("no hex digit is common to every pending request's id")
	return ""
}

// TestDenyOnAnUnmatchedIDDeletesNothing.
func TestDenyOnAnUnmatchedIDDeletesNothing(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	enrolled(t, v, remote, "phone-1")

	err := v.DenyEnrollment("ffffffff", &fakePrompter{})
	if !errors.Is(err, ErrEnrollmentNoSuchRequest) {
		t.Fatalf("DenyEnrollment on an unmatched id = %v, want ErrEnrollmentNoSuchRequest", err)
	}
	if got := exitcode.CodeOf(err); got != exitcode.NotFound {
		t.Fatalf("exit code = %v, want NotFound", got)
	}
	if got := len(pendingFiles(t, v)); got != 1 {
		t.Fatalf("an unmatched deny deleted something: %d requests left, want 1", got)
	}
}

// TestDenyPrunesExpiredRequestsInTheSameCommit. deny is already writing
// and already holds the lock, which is the whole of the TDD's "pruning
// rides the next write" rule.
func TestDenyPrunesExpiredRequestsInTheSameCommit(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	stale := layStaleRequest(t, v, time.Now().Add(-time.Hour))
	beyond := layStaleRequest(t, v, time.Now().Add(2*MaxEnrollmentTTL))

	before, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.DenyEnrollment(req.ID, &fakePrompter{}); err != nil {
		t.Fatalf("DenyEnrollment: %v", err)
	}

	for _, name := range []string{stale, beyond} {
		if _, err := os.Stat(filepath.Join(v.pendingDir(), name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived deny's prune", name)
		}
	}
	after, err := gitrepo.CommitCount(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("deny produced %d commits, want 1 — pruning must ride the same commit", after-before)
	}
}

// layStaleRequest commits a request gage will never honour again, under
// a filename whose epoch says so, and returns its filename.
//
// Hand-written rather than enrolled, because no legitimate path produces
// one — which is the whole reason pruning exists. Committed rather than
// merely written, because every write path resets an unexpectedly dirty
// working tree before it does anything else: an uncommitted stray is
// discarded by that reset, which would make these tests pass without
// pruning existing at all.
func layStaleRequest(t *testing.T, v *Vault, expires time.Time) string {
	t.Helper()
	name := pendingFileName(uuid.NewString(), expires)
	writePending(t, v, name, []byte("not a seal anyone will ever open"))
	if _, err := gitrepo.CommitAll(v.Path, "test: a request nobody will honour"); err != nil {
		t.Fatalf("committing the stale request: %v", err)
	}
	return name
}

// TestDenyWarnsWhenItsPushFails. A silent failed deny is
// indistinguishable from a successful one, and leaves the request live
// for every other device.
func TestDenyWarnsWhenItsPushFails(t *testing.T) {
	v, _, remote := newEnrollFixture(t, "personal", "laptop-1")
	_, req := enrolled(t, v, remote, "phone-1")

	v.remoteSyncer = &fakeSyncer{pushErr: unreachableError()}
	defer func() { v.remoteSyncer = nil }()

	p := &fakePrompter{}
	if err := v.DenyEnrollment(req.ID, p); err != nil {
		t.Fatalf("DenyEnrollment over an unreachable remote must warn, not fail: %v", err)
	}
	if len(p.warnings) == 0 {
		t.Fatal("deny's failed push produced no warning")
	}
	got := strings.Join(p.warnings, "\n")
	if !strings.Contains(got, "still pending") {
		t.Fatalf("deny's failed-push warning does not say the request is still live elsewhere:\n%s", got)
	}
	if files := pendingFiles(t, v); len(files) != 0 {
		t.Fatalf("the local removal did not stand: pending/ holds %v", files)
	}
}
