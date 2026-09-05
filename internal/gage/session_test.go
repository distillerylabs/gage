package gage

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/denmark/gage/internal/gage/config"
	"github.com/denmark/gage/internal/gage/xdgpaths"
)

// newSessionDevice lays out one simulated machine holding several real
// vaults: one set of XDG roots, one device identity per vault, and a
// global-config record for each. Unlike newTestDevice (entry_test.go),
// which models several machines sharing one vault, a session is the
// opposite shape — one machine holding several vaults at once — so the
// config it writes has to accumulate rather than replace.
//
// It returns an opener over the vaults it created, which is exactly what
// SessionConfig.Open wants.
func newSessionDevice(t *testing.T, device string, names ...string) VaultOpener {
	t.Helper()
	isolateXDG(t)

	vaults := map[string]*Vault{}
	g := config.Global{Vaults: map[string]config.VaultEntry{}}
	root := t.TempDir()

	for _, name := range names {
		path := filepath.Join(root, name)
		g.Vaults[name] = config.VaultEntry{Path: path, Type: TypeGit, Device: device, Method: MethodPassphrase}
		writeGlobalConfigForTest(t, g)

		pubkey, err := CreateIdentity(name, device, &fakePrompter{passphrases: []string{testPassphrase}})
		if err != nil {
			t.Fatalf("CreateIdentity for %q: %v", name, err)
		}
		v, err := Create(CreateSpec{
			Name:       name,
			Path:       path,
			Type:       TypeGit,
			Method:     MethodPassphrase,
			Device:     device,
			Recipients: []string{pubkey},
		})
		if err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
		vaults[name] = v
	}

	return func(name string) (*Vault, error) {
		v, ok := vaults[name]
		if !ok {
			return nil, errors.New("no such vault: " + name)
		}
		return v, nil
	}
}

func writeGlobalConfigForTest(t *testing.T, g config.Global) {
	t.Helper()
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	configDirForTest(t)
	if err := config.Write(filepath.Join(dir, "config.toml"), g); err != nil {
		t.Fatal(err)
	}
}

// scriptedPrompter answers unlock requests forever with the same
// passphrase — a session unlocks the same vault repeatedly, so a
// one-answer-per-call script (fakePrompter's) would report "gave up"
// rather than the re-prompt these tests are counting. It also records
// candidate lists and answers them with a scripted choice, which is how
// the ambiguous-query test proves Session went through the Prompter.
type scriptedPrompter struct {
	passphrase string

	unlocks []UnlockRequest
	choices []CandidateList

	// chooseIndex picks which candidate Choose answers with, standing in
	// for a human typing a number at the [1-2] prompt. chooseOverride,
	// when set, answers with an id of the test's choosing instead —
	// what a buggy frontend looks like from the library's side.
	chooseIndex    int
	chooseOverride string
	chooseErr      error

	// warnings records every Warn call, in order — how the M7 index
	// tests prove a page-lock failure is (or isn't) reported, and
	// reported exactly once.
	warnings []string
}

func (p *scriptedPrompter) Unlock(req UnlockRequest) (UnlockResponse, error) {
	p.unlocks = append(p.unlocks, req)
	return UnlockResponse{Kind: KindPassphrase, Passphrase: p.passphrase}, nil
}

func (p *scriptedPrompter) Choose(list CandidateList) (string, error) {
	p.choices = append(p.choices, list)
	if p.chooseErr != nil {
		return "", p.chooseErr
	}
	if p.chooseOverride != "" {
		return p.chooseOverride, nil
	}
	if p.chooseIndex >= len(list.Candidates) {
		return "", errors.New("scriptedPrompter: chooseIndex past the end of the candidate list")
	}
	return list.Candidates[p.chooseIndex].ID, nil
}

func (p *scriptedPrompter) Confirm(prompt string) (bool, error) { return true, nil }
func (p *scriptedPrompter) Value(prompt string) (string, error) { return "", nil }
func (p *scriptedPrompter) Warn(msg string)                     { p.warnings = append(p.warnings, msg) }

// unlockCount reports how many times this prompter has been asked for a
// passphrase for one vault — the number every "did it re-prompt?" test
// below is really asserting on.
func (p *scriptedPrompter) unlockCount(vault string) int {
	n := 0
	for _, req := range p.unlocks {
		if req.Vault == vault {
			n++
		}
	}
	return n
}

// fakeClock is the injectable clock behind the idle timeout: a time a
// test moves by hand instead of waiting for. See SessionConfig.Now.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestSession wires a Session over open with a prompter that always
// answers, and returns both so a test can count re-prompts.
func newTestSession(t *testing.T, open VaultOpener, cfg SessionConfig) (*Session, *scriptedPrompter) {
	t.Helper()
	p := &scriptedPrompter{passphrase: testPassphrase}
	cfg.Open = open
	cfg.Prompter = p
	s := NewSession(cfg)
	t.Cleanup(func() { _ = s.Close() })
	return s, p
}

// TestSessionUseUnlocksOnceThenReusesTheKey is the milestone's core
// property: the unlock cost is paid once per vault per session, not once
// per command.
func TestSessionUseUnlocksOnceThenReusesTheKey(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, p := newTestSession(t, open, SessionConfig{})

	if err := s.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := s.Vault(""); err != nil {
			t.Fatalf("Vault call %d: %v", i, err)
		}
	}
	if err := s.Use("personal"); err != nil {
		t.Fatalf("second Use: %v", err)
	}

	if got := p.unlockCount("personal"); got != 1 {
		t.Errorf("Prompter.Unlock called %d times for one session, want exactly 1", got)
	}
}

// TestSessionCallsTheSameVaultMethodsAsOneShot is the invariant the
// milestone exists to protect: an Identity a Session holds is the same
// thing Vault.Unlock hands a one-shot command, accepted by the same
// methods with the same signatures. Written as a round trip that crosses
// the two modes — inserted through a session-held identity, read back
// through a freshly unlocked one — so a session-only code path could not
// satisfy it.
func TestSessionCallsTheSameVaultMethodsAsOneShot(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, _ := newTestSession(t, open, SessionConfig{Current: "personal"})

	v, ident, err := s.Vault("")
	if err != nil {
		t.Fatalf("Vault: %v", err)
	}
	e := sampleEntry(time.Now())
	id, err := v.Insert(e, false, ident)
	if err != nil {
		t.Fatalf("Insert through a session-held identity: %v", err)
	}

	// The one-shot half: a brand-new Unlock/Close pair around the same
	// Vault, reading what the session wrote.
	oneShot, err := v.Unlock(&fakePrompter{passphrases: []string{testPassphrase}})
	if err != nil {
		t.Fatalf("one-shot Unlock: %v", err)
	}
	defer func() { _ = oneShot.Close() }()

	got, err := v.ReadEntry(id, &oneShot)
	if err != nil {
		t.Fatalf("ReadEntry with a one-shot identity: %v", err)
	}
	if got.Title != e.Title || got.Value != e.Value {
		t.Errorf("read back %q/%q, want %q/%q", got.Title, got.Value, e.Title, e.Value)
	}
}

// TestSessionAddsNoDuplicateVaultMethods is the structural half of the
// same invariant, and it catches what a round trip cannot: a Session
// method that re-implements an entry operation instead of routing to
// Vault's. Every CRUD verb must be declared on *Vault exactly once and
// never on *Session — Session's job is holding identities across calls,
// not operating on entries.
func TestSessionAddsNoDuplicateVaultMethods(t *testing.T) {
	crud := map[string]bool{
		"Insert": true, "ReadEntry": true, "WriteEntry": true, "Remove": true,
		"Rename": true, "Update": true, "EntryIDs": true, "Unlock": true,
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("globbed no .go files; this test is not looking where it thinks it is")
	}

	onVault := map[string]int{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if !crud[fn.Name.Name] {
				continue
			}
			switch recv {
			case "Vault":
				onVault[fn.Name.Name]++
			case "Session":
				t.Errorf("%s: Session.%s duplicates a Vault method; session mode must route to Vault's, not re-implement it",
					name, fn.Name.Name)
			}
		}
	}

	for verb := range crud {
		if onVault[verb] != 1 {
			t.Errorf("Vault.%s is declared %d times, want exactly 1", verb, onVault[verb])
		}
	}
}

// receiverTypeName pulls "Vault" out of both `v *Vault` and `v Vault`.
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// insertTitled inserts a minimal entry with the given title and returns
// its id, for the resolver tests below.
func insertTitled(t *testing.T, v *Vault, ident *Identity, title string) string {
	t.Helper()
	now := NewTimestamp(time.Now())
	id, err := v.Insert(Entry{Title: title, Created: now, Updated: now, UpdatedBy: "laptop-1", Value: "v-" + title}, false, ident)
	if err != nil {
		t.Fatalf("Insert %q: %v", title, err)
	}
	return id.String()
}

// TestSessionResolveAsksThePrompterOnAnAmbiguousQuery is session mode's
// half of the M5 behavior: the resolver returns the same candidate list
// in both modes, one-shot fails with it, and a session has someone to
// ask. Proven at the Session level rather than by driving a terminal.
func TestSessionResolveAsksThePrompterOnAnAmbiguousQuery(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, p := newTestSession(t, open, SessionConfig{Current: "personal"})

	v, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS root account")
	insertTitled(t, v, ident, "AWS IAM backup admin")

	// Candidates come back sorted by title, so index 1 is the root
	// account — asserted below by title rather than assumed here.
	p.chooseIndex = 1

	id, e, err := s.Resolve("", "AWS")
	if err != nil {
		t.Fatalf("Resolve through a session: %v", err)
	}
	if len(p.choices) != 1 {
		t.Fatalf("Prompter.Choose called %d times, want exactly 1", len(p.choices))
	}
	if got := len(p.choices[0].Candidates); got != 2 {
		t.Fatalf("candidate list had %d entries, want 2", got)
	}
	want := p.choices[0].Candidates[1]
	if e.Title != want.Title {
		t.Errorf("Resolve returned %q, want the chosen candidate %q", e.Title, want.Title)
	}
	if id.String() != want.ID {
		t.Errorf("Resolve returned id %s, want the chosen candidate's %s", id, want.ID)
	}
}

// TestSessionResolveRefusesAnIDThatWasNeverOffered: a Prompter answering
// with something outside the list is a frontend bug, and it must not be
// able to select an arbitrary entry.
func TestSessionResolveRefusesAnIDThatWasNeverOffered(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, p := newTestSession(t, open, SessionConfig{Current: "personal"})

	v, ident, err := s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	insertTitled(t, v, ident, "AWS root account")
	insertTitled(t, v, ident, "AWS IAM backup admin")

	// A real entry, but one the "AWS" query never offered as a candidate.
	p.chooseOverride = insertTitled(t, v, ident, "Unrelated")

	if _, _, err := s.Resolve("", "AWS"); !errors.Is(err, ErrNotACandidate) {
		t.Errorf("Resolve error = %v, want ErrNotACandidate", err)
	}
}

// TestSessionLockDropsOneVaultAndLeavesOthersAlone is what makes `lock
// <vault>` useful without exiting: one key gone, the rest of the session
// intact.
func TestSessionLockDropsOneVaultAndLeavesOthersAlone(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, p := newTestSession(t, open, SessionConfig{})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock("personal"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if _, _, err := s.Vault("work"); err != nil {
		t.Fatalf("work after locking personal: %v", err)
	}
	if got := p.unlockCount("work"); got != 1 {
		t.Errorf("work was unlocked %d times, want 1 — locking personal must not disturb it", got)
	}

	if _, _, err := s.Vault("personal"); err != nil {
		t.Fatalf("personal after lock: %v", err)
	}
	if got := p.unlockCount("personal"); got != 2 {
		t.Errorf("personal was unlocked %d times, want 2 (the re-prompt after lock)", got)
	}
}

// TestSessionLockWithNoVaultLocksAll is the decision recorded in the M6
// plan: bare `lock` means every vault this session holds.
func TestSessionLockWithNoVaultLocksAll(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, p := newTestSession(t, open, SessionConfig{})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock(): %v", err)
	}

	for _, st := range s.Status() {
		if st.Unlocked {
			t.Errorf("%s still unlocked after a bare Lock()", st.Name)
		}
	}

	if _, _, err := s.Vault("personal"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Vault("work"); err != nil {
		t.Fatal(err)
	}
	if got := p.unlockCount("personal"); got != 2 {
		t.Errorf("personal unlocked %d times, want 2", got)
	}
	if got := p.unlockCount("work"); got != 2 {
		t.Errorf("work unlocked %d times, want 2", got)
	}
}

// TestSessionStatusReportsMultipleVaults covers `status`/`whoami`'s data:
// every vault touched this session, with its real lock state.
func TestSessionStatusReportsMultipleVaults(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, _ := newTestSession(t, open, SessionConfig{})

	if len(s.Status()) != 0 {
		t.Errorf("a fresh session reports %d vaults, want none", len(s.Status()))
	}

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock("personal"); err != nil {
		t.Fatal(err)
	}

	got := s.Status()
	if len(got) != 2 {
		t.Fatalf("Status reported %d vaults, want 2: %+v", len(got), got)
	}
	// Sorted by name: personal, work.
	if got[0].Name != "personal" || got[0].Unlocked {
		t.Errorf("personal = %+v, want locked", got[0])
	}
	if got[1].Name != "work" || !got[1].Unlocked {
		t.Errorf("work = %+v, want unlocked", got[1])
	}
	if !got[1].Current {
		t.Errorf("work should be the current vault after `use work`: %+v", got[1])
	}
	if got[1].LastUsed.IsZero() {
		t.Error("an unlocked vault should carry a last-used time")
	}
}

// TestSessionAdHocVaultDoesNotChangeCurrent is the `-u|--use NAME` path
// inside a session: it unlocks and operates against another vault without
// moving the session out from under the next bare command.
func TestSessionAdHocVaultDoesNotChangeCurrent(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, p := newTestSession(t, open, SessionConfig{})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	v, _, err := s.Vault("work")
	if err != nil {
		t.Fatalf("ad-hoc Vault(\"work\"): %v", err)
	}
	if v.Name != "work" {
		t.Errorf("ad-hoc call operated against %q, want work", v.Name)
	}
	if p.unlockCount("work") != 1 {
		t.Errorf("work was unlocked %d times, want 1 (the ad-hoc prompt)", p.unlockCount("work"))
	}
	if s.Current() != "personal" {
		t.Errorf("current vault = %q, want it unchanged at personal", s.Current())
	}

	v, _, err = s.Vault("")
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "personal" {
		t.Errorf("the next bare call went to %q, want personal", v.Name)
	}
}

// TestSessionCloseClosesEveryHeldIdentity is the exit/EOF path: nothing
// is left holding key material when the session ends.
func TestSessionCloseClosesEveryHeldIdentity(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, _ := newTestSession(t, open, SessionConfig{})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("work"); err != nil {
		t.Fatal(err)
	}
	_, personal, err := s.Vault("personal")
	if err != nil {
		t.Fatal(err)
	}
	_, work, err := s.Vault("work")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !personal.Closed() || !work.Closed() {
		t.Errorf("Close left an identity open: personal closed=%v, work closed=%v", personal.Closed(), work.Closed())
	}
	// Closing twice is safe — the REPL defers Close and also reaches it
	// on the `exit` path.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestSessionIdleTimeoutRelocksAndRePrompts drives the injectable clock
// past idle_timeout and asserts the next call behaves exactly as if Lock
// had been called.
func TestSessionIdleTimeoutRelocksAndRePrompts(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	clock := newFakeClock()
	s, p := newTestSession(t, open, SessionConfig{IdleTimeout: 10 * time.Minute, Now: clock.now})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}

	// Just inside the window: still unlocked, no re-prompt.
	clock.advance(9 * time.Minute)
	if _, _, err := s.Vault(""); err != nil {
		t.Fatal(err)
	}
	if got := p.unlockCount("personal"); got != 1 {
		t.Fatalf("unlocked %d times before the timeout elapsed, want 1", got)
	}

	// Past it: the next call re-prompts.
	clock.advance(11 * time.Minute)
	if _, _, err := s.Vault(""); err != nil {
		t.Fatal(err)
	}
	if got := p.unlockCount("personal"); got != 2 {
		t.Errorf("unlocked %d times after the idle timeout, want 2 (a re-prompt)", got)
	}
}

// TestIdleTimeoutIsVisibleToStatusWithoutACommand: the timeout is
// evaluated at the start of every Session call, Status included, so the
// prompt's lock indicator goes stale the moment the clock says so rather
// than only after the next data command.
func TestIdleTimeoutIsVisibleToStatusWithoutACommand(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	clock := newFakeClock()
	s, _ := newTestSession(t, open, SessionConfig{IdleTimeout: time.Minute, Now: clock.now})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	if !s.Status()[0].Unlocked {
		t.Fatal("vault should be unlocked immediately after use")
	}

	clock.advance(2 * time.Minute)
	if s.Status()[0].Unlocked {
		t.Error("Status still reports unlocked after the idle timeout elapsed")
	}
}

// TestIdleTimeoutClosesTheIdentityLikeAnExplicitLock is the "same code
// path, not a parallel one" bullet: an aged-out vault's key is released
// through Identity.Close exactly as `lock` releases it.
func TestIdleTimeoutClosesTheIdentityLikeAnExplicitLock(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	clock := newFakeClock()
	s, _ := newTestSession(t, open, SessionConfig{IdleTimeout: time.Minute, Now: clock.now})

	_, ident, err := s.Vault("personal")
	if err != nil {
		t.Fatal(err)
	}
	if ident.Closed() {
		t.Fatal("a freshly unlocked identity reports itself closed")
	}

	clock.advance(2 * time.Minute)
	s.Status()

	if !ident.Closed() {
		t.Error("the idle timeout dropped the vault without closing its Identity")
	}
}

// TestIdleTimeoutNeverInterruptsAnInFlightCommand is the M6 plan's
// decision made testable: the timeout is evaluated between commands, so
// a command built from several Session calls — take the vault, then
// resolve a query in it — can't have its key dropped halfway through,
// however long it runs.
func TestIdleTimeoutNeverInterruptsAnInFlightCommand(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	clock := newFakeClock()
	s, p := newTestSession(t, open, SessionConfig{IdleTimeout: time.Minute, Now: clock.now})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}

	func() {
		defer s.InCommand()()

		_, ident, err := s.Vault("")
		if err != nil {
			t.Fatal(err)
		}
		// The command takes longer than the whole idle timeout.
		clock.advance(5 * time.Minute)

		_, again, err := s.Vault("")
		if err != nil {
			t.Fatalf("second call within one command: %v", err)
		}
		if ident.Closed() {
			t.Error("the idle timeout closed an identity in the middle of a command")
		}
		if again != ident {
			t.Error("the second call within one command got a different identity")
		}
	}()

	if got := p.unlockCount("personal"); got != 1 {
		t.Errorf("unlocked %d times during one command, want 1", got)
	}
	// Bracketing a command defers the timeout, it doesn't disable it: the
	// vault was last used at the end of that command, and idling from
	// there still re-locks it.
	clock.advance(2 * time.Minute)
	if s.Status()[0].Unlocked {
		t.Error("the vault never re-locked after the command ended and the session went idle")
	}
}

// TestIdleTimeoutDisabledWhenZero: an unset idle_timeout means "never
// re-lock on time", not "re-lock immediately".
func TestIdleTimeoutDisabledWhenZero(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	clock := newFakeClock()
	s, p := newTestSession(t, open, SessionConfig{Now: clock.now})

	if err := s.Use("personal"); err != nil {
		t.Fatal(err)
	}
	clock.advance(30 * 24 * time.Hour)
	if _, _, err := s.Vault(""); err != nil {
		t.Fatal(err)
	}
	if got := p.unlockCount("personal"); got != 1 {
		t.Errorf("unlocked %d times with the idle timeout disabled, want 1", got)
	}
}

// TestSessionWithNoCurrentVaultSaysSo: a bare command before any `use`
// reports the missing selection rather than prompting for a passphrase
// nobody can match.
func TestSessionWithNoCurrentVaultSaysSo(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, p := newTestSession(t, open, SessionConfig{})

	_, _, err := s.Vault("")
	if !errors.Is(err, ErrNoCurrentVault) {
		t.Errorf("Vault(\"\") error = %v, want ErrNoCurrentVault", err)
	}
	if len(p.unlocks) != 0 {
		t.Errorf("prompted for a passphrase %d times with no vault selected, want 0", len(p.unlocks))
	}
}

// TestSessionLockUnknownVaultReports: `lock nope` is a user error, not a
// silent no-op.
func TestSessionLockUnknownVaultReports(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal")
	s, _ := newTestSession(t, open, SessionConfig{})

	if err := s.Lock("nope"); err == nil {
		t.Error("locking a vault this session never touched should report it")
	}
}

// TestConcurrentlyHeldIdentitiesDoNotSharePages is the platform-neutral
// guard on what a session made reachable for the first time: two vaults
// unlocked at once, and so two keys page-locked at once.
//
// Page locks are not reference counted and act on whole pages, so two
// keys sharing one page share a single lock and the first Close releases
// the survivor's protection too. On Linux and macOS munlock reports that
// as success, which is why this is asserted on the addresses rather than
// on Close's error: without it the invariant would hold only where
// Windows happens to complain (M6 found it exactly that way, as an
// ERROR_NOT_LOCKED from the second Identity.Close), and a regression
// would again be invisible on two platforms out of three.
func TestConcurrentlyHeldIdentitiesDoNotSharePages(t *testing.T) {
	open := newSessionDevice(t, "laptop-1", "personal", "work")
	s, _ := newTestSession(t, open, SessionConfig{})

	_, personal, err := s.Vault("personal")
	if err != nil {
		t.Fatal(err)
	}
	_, work, err := s.Vault("work")
	if err != nil {
		t.Fatal(err)
	}
	if personal.st == nil || work.st == nil || len(personal.st.secret) == 0 || len(work.st.secret) == 0 {
		t.Fatal("an unlocked identity is holding no key material; this test would pass vacuously")
	}

	pg := uintptr(os.Getpagesize())
	// #nosec G103 -- addresses are read only to bucket them by page.
	pPage := uintptr(unsafe.Pointer(&personal.st.secret[0])) &^ (pg - 1)
	// #nosec G103 -- see above.
	wPage := uintptr(unsafe.Pointer(&work.st.secret[0])) &^ (pg - 1)
	if pPage == wPage {
		t.Errorf("two simultaneously held identities' keys share page %#x; "+
			"closing either would release the other's page lock", pPage)
	}

	// And the consequence that surfaced on Windows: closing both in turn
	// succeeds, rather than the second reporting an already-unlocked
	// page.
	if err := personal.Close(); err != nil {
		t.Fatalf("closing the first identity: %v", err)
	}
	if err := work.Close(); err != nil {
		t.Errorf("closing the second identity after the first: %v", err)
	}
}
