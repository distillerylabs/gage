package main

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/denmark/gage/internal/gage"
)

// failOnUnlockPrompter fails the test the instant it's asked for a
// passphrase — the M13 plan's "never completes into a silent unlock"
// guarantee, asserted the way the plan's own test list specifies: with a
// fake Prompter that fails the test if invoked, rather than merely
// counting calls afterwards.
type failOnUnlockPrompter struct{ t *testing.T }

func (p *failOnUnlockPrompter) Unlock(req gage.UnlockRequest) (gage.UnlockResponse, error) {
	p.t.Fatalf("completion triggered an unlock for vault %q", req.Vault)
	return gage.UnlockResponse{}, nil
}
func (p *failOnUnlockPrompter) Value(prompt string) (string, error) { return "", nil }
func (p *failOnUnlockPrompter) Confirm(prompt string) (bool, error) { return true, nil }
func (p *failOnUnlockPrompter) ConfirmDefaultYes(string) (bool, error) {
	return true, nil
}
func (p *failOnUnlockPrompter) ConfirmRecipientChange(gage.RecipientChangeWarning) (bool, error) {
	return true, nil
}
func (p *failOnUnlockPrompter) Choose(gage.CandidateList) (string, error) { return "", nil }
func (p *failOnUnlockPrompter) Warn(string)                               {}

// newSessionApp builds an App with a live Session over global config's
// registered vaults — the same construction bare `gage`'s session mode
// uses (openSessionRun) — so completion's candidate functions can be
// exercised against a real Session without a terminal.
func newSessionApp(t *testing.T, p gage.Prompter) *App {
	t.Helper()
	app := testApp()
	app.Prompter = p
	sess, _, cleanup, err := openSessionRun(app, p)
	if err != nil {
		t.Fatalf("openSessionRun: %v", err)
	}
	t.Cleanup(cleanup)
	app.Session = sess
	return app
}

// stdoutString returns what a test's App has written to stdout so far.
// testApp/newSessionApp always give App.Out a *bytes.Buffer; this just
// names the assertion so callers don't repeat the comma-ok form.
func stdoutString(t *testing.T, app *App) string {
	t.Helper()
	buf, ok := app.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("app.Out is a %T, not a *bytes.Buffer", app.Out)
	}
	return buf.String()
}

// --- prefixMatches -----------------------------------------------------

func TestPrefixMatchesReturnsEveryPrefixHit(t *testing.T) {
	got := prefixMatches([]string{"lock", "log", "ls", "use"}, "l")
	want := []string{"lock", "log", "ls"}
	if !sliceEqual(got, want) {
		t.Errorf("prefixMatches = %v, want %v", got, want)
	}
}

// TestPrefixMatchesRejectsSubstring pins the "prefix only" decision
// against regressing toward gage.resolveTitleAndUUID's substring rule.
func TestPrefixMatchesRejectsSubstring(t *testing.T) {
	got := prefixMatches([]string{"identity"}, "enti")
	if len(got) != 0 {
		t.Errorf("prefixMatches(%q) against a mid-string match = %v, want none", "enti", got)
	}
}

func TestPrefixMatchesDedupesAndSorts(t *testing.T) {
	got := prefixMatches([]string{"use", "use", "lock"}, "")
	want := []string{"lock", "use"}
	if !sliceEqual(got, want) {
		t.Errorf("prefixMatches = %v, want %v", got, want)
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- topLevelSessionNames ------------------------------------------------

// TestTopLevelSessionNamesExcludesInitAndClone pins Q-CMD-AVAILABILITY
// against this new surface: init/clone must never be completable or
// abbreviation-reachable in a session.
func TestTopLevelSessionNamesExcludesInitAndClone(t *testing.T) {
	names := topLevelSessionNames()
	for _, bad := range []string{"init", "clone"} {
		for _, n := range names {
			if n == bad {
				t.Errorf("topLevelSessionNames() includes %q, which is one-shot only", bad)
			}
		}
	}
}

func TestTopLevelSessionNamesIncludesGroupsAndAliases(t *testing.T) {
	names := topLevelSessionNames()
	for _, want := range []string{"identity", "vault", "recipient", "git", "auth", "use", "status", "whoami", "search", "grep"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("topLevelSessionNames() missing %q; got %v", want, names)
		}
	}
}

// --- Tab completion of a partial top-level name --------------------------

// TestCompletingPartialSessionCommandsMatchesExactly is the M13 plan's
// own wording: completing "us"/"loc"/"stat" returns exactly the matching
// candidate(s), each a single unambiguous match.
func TestCompletingPartialSessionCommandsMatchesExactly(t *testing.T) {
	for _, tc := range []struct{ partial, want string }{
		{"us", "use"},
		{"loc", "lock"},
		{"stat", "status"},
	} {
		got := prefixMatches(topLevelSessionNames(), tc.partial)
		if !sliceEqual(got, []string{tc.want}) {
			t.Errorf("completing %q = %v, want exactly [%q]", tc.partial, got, tc.want)
		}
	}
}

// --- resolveSessionCommandName -------------------------------------------

func TestResolveSessionCommandNameExactNameUnchanged(t *testing.T) {
	got, err := resolveSessionCommandName("use")
	if err != nil || got != "use" {
		t.Errorf("resolveSessionCommandName(%q) = (%q, %v), want (\"use\", nil)", "use", got, err)
	}
}

func TestResolveSessionCommandNameExactAliasUnchanged(t *testing.T) {
	got, err := resolveSessionCommandName("whoami")
	if err != nil || got != "whoami" {
		t.Errorf("resolveSessionCommandName(%q) = (%q, %v), want (\"whoami\", nil)", "whoami", got, err)
	}
}

func TestResolveSessionCommandNameUnambiguousAbbreviation(t *testing.T) {
	got, err := resolveSessionCommandName("ident")
	if err != nil {
		t.Fatalf("resolveSessionCommandName(%q): %v", "ident", err)
	}
	if got != "identity" {
		t.Errorf("resolveSessionCommandName(%q) = %q, want %q", "ident", got, "identity")
	}
}

func TestResolveSessionCommandNameAmbiguousAbbreviationReportsCandidates(t *testing.T) {
	_, err := resolveSessionCommandName("l")
	var amb *ambiguousCommandError
	if err == nil {
		t.Fatal("resolveSessionCommandName(\"l\") returned no error, want an ambiguous-command error")
	}
	if !errorsAs(err, &amb) {
		t.Fatalf("resolveSessionCommandName(\"l\") error = %v (%T), want *ambiguousCommandError", err, err)
	}
	for _, want := range []string{"lock", "log", "ls"} {
		if !strings.Contains(amb.Error(), want) {
			t.Errorf("ambiguous error %q doesn't name candidate %q", amb.Error(), want)
		}
	}
}

// TestResolveSessionCommandNameSubstringNotAbbreviation pins the same
// "prefix only" rule against the abbreviation-dispatch half.
func TestResolveSessionCommandNameSubstringNotAbbreviation(t *testing.T) {
	got, err := resolveSessionCommandName("enti")
	if err != nil {
		t.Fatalf("resolveSessionCommandName(%q): %v", "enti", err)
	}
	if got != "enti" {
		t.Errorf("resolveSessionCommandName(%q) = %q, want unchanged (no prefix match)", "enti", got)
	}
}

func TestResolveSessionCommandNameUnknownWordUnchanged(t *testing.T) {
	got, err := resolveSessionCommandName("nope")
	if err != nil || got != "nope" {
		t.Errorf("resolveSessionCommandName(%q) = (%q, %v), want (%q, nil)", "nope", got, err, "nope")
	}
}

// TestResolveSessionCommandNameNeverReachesInitOrClone: "ini"/"clo" have
// no session-visible match to expand into, so they fall through
// unchanged to the existing unknown-command path rather than resolving
// to a one-shot-only command.
func TestResolveSessionCommandNameNeverReachesInitOrClone(t *testing.T) {
	for _, word := range []string{"ini", "clo"} {
		got, err := resolveSessionCommandName(word)
		if err != nil || got != word {
			t.Errorf("resolveSessionCommandName(%q) = (%q, %v), want unchanged", word, got, err)
		}
	}
}

// errorsAs is errors.As spelled locally so this file doesn't need a
// second import line for one call.
func errorsAs(err error, target **ambiguousCommandError) bool {
	if e, ok := err.(*ambiguousCommandError); ok {
		*target = e
		return true
	}
	return false
}

// --- runSessionCommand abbreviation dispatch (end to end) ---------------

// TestRunSessionCommandDispatchesAnUnambiguousAbbreviation calls
// runSessionCommand directly (the M13 plan's own test-list wording) with
// an abbreviation of a top-level command and checks it ran the same
// command a full spelling would.
func TestRunSessionCommandDispatchesAnUnambiguousAbbreviation(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	if err := runSessionCommand(app, []string{"stat"}); err != nil {
		t.Fatalf("runSessionCommand([\"stat\"]): %v", err)
	}
	out := stdoutString(t, app)
	if !strings.Contains(out, "no vaults have been unlocked") {
		t.Errorf("\"stat\" didn't dispatch to `status`:\n%s", out)
	}
}

// TestRunSessionCommandRejectsAnAmbiguousAbbreviation is the plan's other
// abbreviation test: candidates reported, nothing run, session still
// alive (a returned error, not a terminated session — the same shape an
// unknown command already has).
func TestRunSessionCommandRejectsAnAmbiguousAbbreviation(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	err := runSessionCommand(app, []string{"l"})
	if err == nil {
		t.Fatal("runSessionCommand([\"l\"]) succeeded, want an ambiguous-command error")
	}
	for _, want := range []string{"lock", "log", "ls"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguous dispatch error %q doesn't name %q", err.Error(), want)
		}
	}
	out := stdoutString(t, app)
	if out != "" {
		t.Errorf("an ambiguous abbreviation ran something: stdout = %q", out)
	}
	// The session itself must still be usable — this is a REPL-level
	// rejection, not a process exit.
	if err := runSessionCommand(app, []string{"status"}); err != nil {
		t.Fatalf("session didn't survive an ambiguous abbreviation: %v", err)
	}
}

// --- completionWords -------------------------------------------------

func TestCompletionWordsMidWord(t *testing.T) {
	words, partial := completionWords("identi")
	if len(words) != 0 || partial != "identi" {
		t.Errorf("completionWords(%q) = (%v, %q), want (nil, %q)", "identi", words, partial, "identi")
	}
}

func TestCompletionWordsAfterASpace(t *testing.T) {
	words, partial := completionWords("identity ")
	if !sliceEqual(words, []string{"identity"}) || partial != "" {
		t.Errorf("completionWords(%q) = (%v, %q), want ([identity], \"\")", "identity ", words, partial)
	}
}

func TestCompletionWordsMultipleWordsPartialLast(t *testing.T) {
	words, partial := completionWords("show --use pers")
	if !sliceEqual(words, []string{"show", "--use"}) || partial != "pers" {
		t.Errorf("completionWords(...) = (%v, %q), want ([show --use], \"pers\")", words, partial)
	}
}

// --- flagCandidates / subcommandCandidates -------------------------------

// TestFlagCandidatesMatchesTheRealCobraCommand asserts against the real
// *cobra.Command's own registered flags, per the M13 plan's flag test.
func TestFlagCandidatesMatchesTheRealCobraCommand(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	target, _, err := root.Find([]string{"insert"})
	if err != nil {
		t.Fatal(err)
	}

	got := flagCandidates(target)
	for _, want := range []string{"--use", "-u", "--description", "--multiline", "-m", "--value-stdin", "--edit", "-e", "--force", "-f", "--field"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("flagCandidates(insert) missing %q; got %v", want, got)
		}
	}
	// A flag belonging to a different command must not leak in.
	for _, g := range got {
		if g == "--to-vault" {
			t.Errorf("flagCandidates(insert) included --to-vault, which belongs to mv/cp")
		}
	}
}

func TestSubcommandCandidatesListsIdentityChildren(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	target, _, err := root.Find([]string{"identity"})
	if err != nil {
		t.Fatal(err)
	}

	got := subcommandCandidates(target)
	want := []string{"add", "enroll", "list"}
	if !sliceEqual(got, want) {
		t.Errorf("subcommandCandidates(identity) = %v, want %v", got, want)
	}
}

// --- vaultNameCandidates -------------------------------------------------

func TestVaultNameCandidatesListsRegisteredVaults(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")

	got := vaultNameCandidates()
	want := []string{"personal", "work"}
	if !sliceEqual(got, want) {
		t.Errorf("vaultNameCandidates() = %v, want %v", got, want)
	}
}

// --- entryTitleCandidates / completionCandidates over a real Session ----

// TestEntryTitleCandidatesNoSessionIsNil covers one-shot mode (no
// App.Session at all), which should never be asked in practice but must
// not panic if it is.
func TestEntryTitleCandidatesNoSessionIsNil(t *testing.T) {
	app := testApp()
	if got := entryTitleCandidates(app); got != nil {
		t.Errorf("entryTitleCandidates with no session = %v, want nil", got)
	}
}

// TestAbbreviationDoesNotReachSubcommands pins the "different reach"
// decision: abbreviation dispatch only ever rewrites the first token, so
// "identity ad" is not treated as an abbreviation of "identity add" —
// only Tab completion reaches subcommands (see
// TestCompletingSubcommandListsIdentityChildren). Cobra itself (with
// prefix matching off, the default this codebase relies on) falls back
// to printing `identity`'s own help for an unrecognized second word
// rather than erroring, so the assertion is "landed on identity's help,
// never ran add's prompt", not "returned an error".
func TestAbbreviationDoesNotReachSubcommands(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	if err := runSessionCommand(app, []string{"identity", "ad"}); err != nil {
		t.Fatalf(`runSessionCommand(["identity", "ad"]): %v`, err)
	}
	out := stdoutString(t, app)
	if !strings.Contains(out, "Manage this device's identities for a vault") {
		t.Errorf(`"identity ad" didn't fall through to identity's own help (proving "ad" never abbreviated "add"):\n%s`, out)
	}
}

// TestCompletingAnEntryQueryWhenLockedNeverUnlocks is the M13 plan's own
// test-list wording: a fake Prompter that fails the test if invoked.
func TestCompletingAnEntryQueryWhenLockedNeverUnlocks(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	app := newSessionApp(t, &failOnUnlockPrompter{t: t})

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"show"}, "")
	if len(got) != 0 {
		t.Errorf("completing `show`'s query on a locked vault = %v, want none", got)
	}
}

// TestCompletingAnEntryQueryListsCurrentVaultTitles is the unlocked half:
// titles come from the current vault's index once it's unlocked.
func TestCompletingAnEntryQueryListsCurrentVaultTitles(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	insertEntry(t, "personal", "ProtonMail", "hunter2")
	insertEntry(t, "personal", "AWS", "secret")

	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})
	if err := app.Session.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"show"}, "")
	want := []string{"AWS", "ProtonMail"}
	if !sliceEqual(got, want) {
		t.Errorf("completing `show`'s query on an unlocked vault = %v, want %v", got, want)
	}
}

// --- completionCandidates: vault-name positions --------------------------

func TestCompletingUseArgumentListsVaultNames(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"use"}, "")
	want := []string{"personal", "work"}
	if !sliceEqual(got, want) {
		t.Errorf("completing `use`'s argument = %v, want %v", got, want)
	}
}

func TestCompletingUseFlagValueListsVaultNames(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"show", "--use"}, "")
	want := []string{"personal", "work"}
	if !sliceEqual(got, want) {
		t.Errorf("completing --use's value = %v, want %v", got, want)
	}
}

func TestCompletingLockArgumentListsVaultNames(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"lock"}, "")
	want := []string{"personal", "work"}
	if !sliceEqual(got, want) {
		t.Errorf("completing `lock`'s argument = %v, want %v", got, want)
	}
}

// TestCompletingUseArgumentIsIndependentOfUnlockState is the "independent
// of which vaults are currently unlocked" half of the M13 plan's
// vault-name test: unlocking one of the two vaults doesn't change the
// candidate set at all, since it comes from global config, never from
// Session.Status.
func TestCompletingUseArgumentIsIndependentOfUnlockState(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})
	if err := app.Session.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"use"}, "")
	want := []string{"personal", "work"}
	if !sliceEqual(got, want) {
		t.Errorf("completing `use`'s argument with \"personal\" already unlocked = %v, want %v (unchanged)", got, want)
	}
}

// TestCompletingQueryAfterAUseFlagStillOffersEntryTitles pins the
// flag-before-positional ordering: "show --use work <query>" is still
// completing the query at position 0, not swallowed as a later
// positional because --use/work precede it on the line. It also
// reconfirms "current vault only": --use names "work", but the
// candidates are still "personal"'s (the session's current vault), never
// "work"'s.
func TestCompletingQueryAfterAUseFlagStillOffersEntryTitles(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	initEntryTestVault(t, "work")
	insertEntry(t, "personal", "ProtonMail", "hunter2")
	insertEntry(t, "work", "GitHub", "tok3n")

	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})
	if err := app.Session.Use("personal"); err != nil {
		t.Fatalf("Use: %v", err)
	}

	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"show", "--use", "work"}, "")
	want := []string{"ProtonMail"}
	if !sliceEqual(got, want) {
		t.Errorf("completing the query after --use work = %v, want %v (personal's titles, not work's)", got, want)
	}
}

// --- completionCandidates: top level / subcommands -----------------------

func TestCompletingTopLevelListsSessionVisibleNames(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	got := completionCandidates(app, root, nil, "us")
	// completionCandidates itself returns the whole top-level set;
	// prefixMatches (the caller's job, exercised by sessionCompleter.Do)
	// is what narrows it. Confirm the set at least contains "use" and
	// excludes init/clone.
	found := false
	for _, g := range got {
		if g == "use" {
			found = true
		}
		if g == "init" || g == "clone" {
			t.Errorf("top-level completion candidates included one-shot-only %q", g)
		}
	}
	if !found {
		t.Errorf("top-level completion candidates missing \"use\": %v", got)
	}
}

func TestCompletingSubcommandListsIdentityChildren(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"identity"}, "")
	want := []string{"add", "enroll", "list"}
	if !sliceEqual(got, want) {
		t.Errorf("completing `identity `'s subcommand = %v, want %v", got, want)
	}
}

func TestCompletingFlagPositionListsCommandFlags(t *testing.T) {
	app := testApp()
	root := NewRootCmd(app)
	got := completionCandidates(app, root, []string{"insert"}, "--")
	found := false
	for _, g := range got {
		if g == "--force" {
			found = true
		}
	}
	if !found {
		t.Errorf("completing `insert --`'s flag position missing --force: %v", got)
	}
}

// --- sessionCompleter.Do: the one thin wiring test -----------------------

// TestSessionCompleterDoWiresCandidatesIntoReadlinesContract is the
// milestone's single test of the AutoCompleter.Do wiring itself, per the
// plan's "candidate-producing functions are plain, typed, and callable
// without a terminal ... Do itself gets one thin test" decision.
// Everything about *which* candidates are right for a given position is
// covered above, directly against completionCandidates/prefixMatches;
// this only checks that Do wires them into readline's suffix/length
// contract correctly (see chzyer/readline's AutoCompleter doc comment)
// across the three shapes that contract has to handle: a single match
// (write the suffix), several (list every suffix), and none (a
// substring hit that isn't a prefix hit, offering nothing).
func TestSessionCompleterDoWiresCandidatesIntoReadlinesContract(t *testing.T) {
	isolateXDG(t)
	initEntryTestVault(t, "personal")
	app := newSessionApp(t, &fakePrompter{passphrases: []string{testPassphrase}})
	c := newSessionCompleter(app)

	for _, tc := range []struct {
		name    string
		partial string
		want    []string // full candidate words, not raw suffixes
	}{
		{"single match completes the suffix", "u", []string{"use"}},
		{"several matches list every suffix", "l", []string{"lock", "log", "ls"}},
		{"a substring (not prefix) match offers nothing", "enti", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := []rune(tc.partial)
			newLine, length := c.Do(line, len(line))
			if length != len(line) {
				t.Errorf("Do(%q) length = %d, want %d (the partial word already typed)", tc.partial, length, len(line))
			}
			var got []string
			for _, nl := range newLine {
				got = append(got, tc.partial+string(nl))
			}
			if !sliceEqual(got, tc.want) {
				t.Errorf("Do(%q) candidates = %v, want %v", tc.partial, got, tc.want)
			}
		})
	}
}
