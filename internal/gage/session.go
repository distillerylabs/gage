package gage

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/distillerylabs/gage/internal/gage/exitcode"
)

// ErrNoCurrentVault is what every current-vault-scoped Session call
// returns when nothing has been `use`d yet and no default was configured.
var ErrNoCurrentVault = errors.New("gage: no vault is selected in this session")

// ErrNotACandidate means a Prompter answered a candidate list with an id
// that wasn't in it. That's a frontend bug rather than a human's mistake,
// but it must not be allowed to select an arbitrary entry, so it's
// refused rather than trusted.
var ErrNotACandidate = errors.New("gage: the chosen id is not one of the candidates offered")

// VaultOpener turns a vault name into the Vault it names. It's injected
// rather than implemented here because "which names are registered on
// this machine, and where do they live" is global-config knowledge the
// frontend already resolves for one-shot mode (see cmd/gage's
// resolveVaultEntry) — Session's job is holding identities across calls,
// not re-deriving the registry.
//
// It's only ever called with a non-empty name: Session resolves "the
// current vault" to a concrete name itself.
type VaultOpener func(name string) (*Vault, error)

// SessionConfig is everything NewSession needs. Every field except Now is
// required; Now defaults to the real clock.
type SessionConfig struct {
	// Open resolves a vault name to a Vault.
	Open VaultOpener

	// Prompter is what unlocking (and an ambiguous query's candidate
	// list) asks a human through — the same interface, and in cmd/gage
	// the same implementation, one-shot mode uses.
	Prompter Prompter

	// IdleTimeout re-locks a vault that hasn't been touched for this
	// long. Zero or negative disables it. See "Why an idle timeout still
	// matters despite process-scoped keys".
	IdleTimeout time.Duration

	// Current seeds the session's current vault — normally global
	// config's `current`, so a session behaves like one-shot mode until
	// someone types `use`. Naming a vault here does not unlock it; the
	// first command that needs a key does.
	Current string

	// Now is the clock the idle timeout reads. Injected rather than
	// calling time.Now directly so a test can simulate elapsed time
	// instead of sleeping through it; nil means the real clock. A plain
	// function is enough because the timeout is evaluated at the start of
	// each call rather than by a background timer, so nothing here ever
	// needs to sleep or schedule (see the M6 plan's "Idle-timeout clock
	// injection").
	Now func() time.Time
}

// Session holds one or more vaults' unlocked Identity values in memory
// for longer than a single command — the library type behind both
// cmd/gage's REPL and any future GUI/TUI's "unlock once, do several
// things" mode. See "Session model" in the design doc.
//
// It adds no new memory-protection mechanics: Use calls the same
// Vault.Unlock a one-shot command does, and Lock (and the idle timeout)
// calls the same Identity.Close. What differs between the two modes is
// only how many times that pair runs around a Vault call, never which
// Vault methods run or what they're handed.
//
// A Session is not safe for concurrent use. The REPL drives one command
// at a time, which is also why the idle timeout is evaluated at the start
// of each call rather than firing from a goroutine: a re-lock can never
// land in the middle of an in-flight command.
type Session struct {
	open        VaultOpener
	prompter    Prompter
	idleTimeout time.Duration
	now         func() time.Time

	current string
	vaults  map[string]*heldVault

	// depth counts nested public calls, so the idle timeout is evaluated
	// on entering the outermost one and not again until it returns. See
	// enter and InCommand.
	depth int
}

// enter marks the start of one Session call and returns its end.
//
// The idle timeout is applied here, on the way in, and only for the
// outermost call: a re-lock is a thing that happens *between*
// operations, never inside one. Without the depth check, an operation
// built from several Session calls (a command that takes a vault and
// then resolves a query in it) could have the key dropped out from under
// its own second call.
func (s *Session) enter() func() {
	if s.depth == 0 {
		s.expireIdle()
	}
	s.depth++
	return func() { s.depth-- }
}

// InCommand widens that guarantee to one whole command: a frontend
// running several Session calls as a single unit of work — cmd/gage's
// REPL runs one typed line that way — brackets them with this, and the
// idle timeout is then evaluated once before the command starts rather
// than between its parts.
//
// This is the decision recorded in the M6 plan ("does an idle re-lock
// interrupt an in-flight command?" — no) made structural rather than
// left to how quickly the parts happen to run.
func (s *Session) InCommand() func() { return s.enter() }

// heldVault is one vault this session has touched. It survives a Lock —
// a locked vault is still a vault the session knows about, which is what
// lets `status` list it as locked and the prompt render 🔒 rather than
// forgetting it ever existed.
//
// ident is a pointer, and a nil one is what "locked" means here. Holding
// the Identity by pointer rather than by value is what keeps a caller
// that still has the *Identity from an earlier call — the REPL hands one
// to each command — looking at the identity it was actually given: it
// stays closed (so using it fails with ErrIdentityClosed, per Close's
// contract) instead of silently becoming whatever key a later re-unlock
// put in its place.
type heldVault struct {
	vault    *Vault
	ident    *Identity
	lastUsed time.Time

	// index is this vault's M7 metadata cache: nil until the first
	// ls/show/search/resolve needs one, discarded (and set back to nil)
	// by the same code path that closes ident — see lockHeld. A vault
	// that's only ever been mutated, never listed or resolved, stays nil
	// for the whole session, which is what keeps insert/edit/rm from
	// forcing a build that nothing has asked for yet.
	index *Index

	// indexWarned gates the one-time page-lock-failure warning for this
	// vault's index (see Index.lockOK and Session.warnIndexLockOnce) so
	// a long session that keeps growing the arena doesn't repeat it.
	// Reset whenever the index is discarded, since a fresh unlock may
	// page-lock differently than the last one did.
	indexWarned bool
}

// VaultState is one row of Session.Status.
type VaultState struct {
	Name     string
	Unlocked bool
	Current  bool
	// LastUsed is when this vault last served a Session call. Zero if it
	// has never been unlocked.
	LastUsed time.Time
}

// NewSession builds a Session over cfg.
func NewSession(cfg SessionConfig) *Session {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Session{
		open:        cfg.Open,
		prompter:    cfg.Prompter,
		idleTimeout: cfg.IdleTimeout,
		now:         now,
		current:     cfg.Current,
		vaults:      map[string]*heldVault{},
	}
}

// Current reports which vault name commands operate on when none is
// named. Empty means nothing is selected.
func (s *Session) Current() string { return s.current }

// Use makes name the session's current vault, unlocking it if this
// session isn't already holding its key. Calling it for a vault that's
// already unlocked re-selects it without re-prompting — that's the whole
// point of a session.
func (s *Session) Use(name string) error {
	if name == "" {
		return exitcode.Wrap(exitcode.Usage, fmt.Errorf("%w: use <vault>", ErrNoCurrentVault))
	}

	held, known := s.vaults[name]
	wasUnlocked := known && held.ident != nil

	if _, _, err := s.Vault(name); err != nil {
		return err
	}
	// `use` means "catch me up" whether or not this session already holds
	// the key. When it doesn't, Vault.Unlock just ran and its automatic
	// pull came with it; when it does, no unlock happened, so the pull
	// has to be asked for here rather than being silently skipped for the
	// vaults a session returns to most often.
	if wasUnlocked {
		held.vault.syncOnUnlock(s.prompter)
	}

	s.current = name
	return nil
}

// Vault returns a vault and its unlocked Identity, unlocking it first if
// needed. An empty name means the session's current vault; naming a vault
// explicitly (the `-u|--use NAME` path) operates against it without
// changing which vault is current.
//
// The returned Identity belongs to the Session and must not be Closed by
// the caller — Lock, the idle timeout, and Session.Close are the only
// things that close it.
func (s *Session) Vault(name string) (*Vault, *Identity, error) {
	defer s.enter()()

	if name == "" {
		name = s.current
	}
	if name == "" {
		return nil, nil, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("%w: run `use <vault>` first, or pass -u/--use", ErrNoCurrentVault))
	}

	held, err := s.hold(name)
	if err != nil {
		return nil, nil, err
	}

	if held.ident == nil {
		ident, err := held.vault.Unlock(s.prompter)
		if err != nil {
			return nil, nil, err
		}
		held.ident = &ident
	}
	held.lastUsed = s.now()
	return held.vault, held.ident, nil
}

// VaultWithoutUnlocking returns a session vault ("" for the current one)
// without unlocking it, registering it with this session the same way
// Vault does so a sync that pulls still invalidates its cached metadata.
//
// It exists for the sync verbs, which per "Sync model" unlock lazily: a
// fast-forward, and a merge of two sides that touched different entries,
// decrypt nothing and so have no business prompting for a passphrase.
// Only a conflict whose resolution has to show plaintext forces an
// unlock, and that is M8b's to ask for.
func (s *Session) VaultWithoutUnlocking(name string) (*Vault, error) {
	defer s.enter()()

	if name == "" {
		name = s.current
	}
	if name == "" {
		return nil, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("%w: run `use <vault>` first, or pass -u/--use", ErrNoCurrentVault))
	}

	held, err := s.hold(name)
	if err != nil {
		return nil, err
	}
	return held.vault, nil
}

// hold returns this session's record for a vault, opening and registering
// it on first sight. It never unlocks: Vault is what does that, on top of
// this.
func (s *Session) hold(name string) (*heldVault, error) {
	if held, ok := s.vaults[name]; ok {
		return held, nil
	}

	v, err := s.open(name)
	if err != nil {
		return nil, err
	}
	held := &heldVault{vault: v}
	// Wired here, once, rather than around each call that might cause a
	// pull: a sync that moves entries/ underneath this session has to
	// invalidate the metadata index built from them, and the vault is
	// what knows when that happened.
	v.onPull = func() { s.discardIndex(held) }
	s.vaults[name] = held
	return held, nil
}

// Lock drops the key material for the named vaults, or — with no name at
// all — for every vault this session holds. The vaults stay listed by
// Status as locked; the next call against one re-prompts.
func (s *Session) Lock(names ...string) error {
	defer s.enter()()

	if len(names) == 0 {
		names = make([]string, 0, len(s.vaults))
		for name := range s.vaults {
			names = append(names, name)
		}
		sort.Strings(names)
	}

	var errs []error
	for _, name := range names {
		held, ok := s.vaults[name]
		if !ok {
			errs = append(errs, exitcode.Newf(exitcode.NotFound,
				"gage: %q is not unlocked in this session", name))
			continue
		}
		if err := s.lockHeld(held); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// lockHeld is the single place a held vault's key is dropped, so an idle
// timeout and an explicit Lock are the same code path rather than two
// that happen to agree today. It's also the single place the vault's
// index is discarded: the index is decrypted metadata and must not
// outlive the identity that produced it, so the two are torn down
// together here rather than by two callers that happen to agree.
func (s *Session) lockHeld(held *heldVault) error {
	s.discardIndex(held)
	if held.ident == nil {
		return nil
	}
	ident := held.ident
	held.ident = nil
	return ident.Close()
}

// discardIndex drops a held vault's cached metadata, if it has any. It's
// the single place that happens, so the three things that invalidate a
// cache — locking the vault, an explicit reindex, and a sync that moved
// entries/ underneath it — can't drift apart.
func (s *Session) discardIndex(held *heldVault) {
	if held.index == nil {
		return
	}
	held.index.discard()
	held.index = nil
	held.indexWarned = false
}

// expireIdle re-locks every vault whose last use is further in the past
// than the idle timeout. It runs at the start of each Session call rather
// than from a timer, so a re-lock is never able to interrupt an
// in-flight command (see the M6 plan's decision on that).
//
// A Close error here has nowhere to go — the caller asked to unlock a
// vault, not to lock one — and the key has been dropped either way, so it
// is deliberately not propagated. See Identity.Close: the only error it
// can return is a failed page *unlock*, after the key is already zeroed.
func (s *Session) expireIdle() {
	if s.idleTimeout <= 0 {
		return
	}
	cutoff := s.now().Add(-s.idleTimeout)
	for _, held := range s.vaults {
		if held.ident != nil && held.lastUsed.Before(cutoff) {
			_ = s.lockHeld(held)
		}
	}
}

// Status reports every vault this session has touched, sorted by name.
// The idle timeout is applied first, so a vault that has aged out reads
// as locked here — which is what keeps the prompt's lock indicator honest
// without a command having to run first.
func (s *Session) Status() []VaultState {
	defer s.enter()()

	names := make([]string, 0, len(s.vaults))
	for name := range s.vaults {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]VaultState, 0, len(names))
	for _, name := range names {
		held := s.vaults[name]
		out = append(out, VaultState{
			Name:     name,
			Unlocked: held.ident != nil,
			Current:  name == s.current,
			LastUsed: held.lastUsed,
		})
	}
	return out
}

// Resolve runs the shared query resolver against a session vault ("" for
// the current one) and, when the query is ambiguous, asks the Prompter
// which candidate was meant instead of failing.
//
// That is the whole difference between the two modes' handling of an
// ambiguous query: the resolver returns the same candidate list either
// way, one-shot mode prints it and fails, and a session has someone to
// ask. See "Addressing entries & the metadata index".
//
// Unlike Vault.Resolve, this is index-backed (M7): matching runs against
// the session's cached titles, built (or reused) by ensureIndex, and
// only the entry that actually wins gets decrypted — a full-vault
// decrypt pass happens at most once per vault per session, on whichever
// of ls/show/search/resolve asks first.
func (s *Session) Resolve(name, query string) (uuid.UUID, Entry, error) {
	v, ident, err := s.Vault(name)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}
	held := s.vaults[v.Name]
	if err := s.ensureIndex(held, ident); err != nil {
		return uuid.Nil, Entry{}, err
	}

	id, err := resolveTitleAndUUID(query, held.index.titles())
	if err == nil {
		e, err := v.ReadEntry(id, ident)
		if err != nil {
			return uuid.Nil, Entry{}, err
		}
		return id, e, nil
	}
	var amb *AmbiguousQueryError
	if !errors.As(err, &amb) {
		return uuid.Nil, Entry{}, err
	}
	if s.prompter == nil {
		// Nobody to ask, so the candidate list is the answer — the same
		// outcome one-shot mode has, rather than a nil dereference.
		return uuid.Nil, Entry{}, err
	}

	chosen, err := s.prompter.Choose(amb.List)
	if err != nil {
		return uuid.Nil, Entry{}, exitcode.Wrap(exitcode.Ambiguous, err)
	}
	if !isCandidate(amb.List, chosen) {
		return uuid.Nil, Entry{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("%w: %q", ErrNotACandidate, chosen))
	}
	chosenID, err := uuid.Parse(chosen)
	if err != nil {
		return uuid.Nil, Entry{}, exitcode.Wrap(exitcode.Internal,
			fmt.Errorf("gage: candidate id %q is not a uuid: %w", chosen, err))
	}

	e, err := v.ReadEntry(chosenID, ident)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}
	return chosenID, e, nil
}

// List returns vault's ("" for the current one) entries as ListEntry
// rows, sorted by title then id — ls's rendering, backed by the same
// index Resolve and Search build and reuse.
func (s *Session) List(name string) ([]ListEntry, error) {
	v, ident, err := s.Vault(name)
	if err != nil {
		return nil, err
	}
	held := s.vaults[v.Name]
	if err := s.ensureIndex(held, ident); err != nil {
		return nil, err
	}
	return held.index.list(), nil
}

// Search matches pattern against vault's ("" for the current one)
// entries: title and description come from the index (built or reused
// exactly like List/Resolve), while the body-text half always decrypts
// fresh — see the M7 plan's "Decisions made" on why a secret value never
// enters the cache. The very first search for a vault in a session
// builds the index from the same decrypt pass it needs for its own
// body-text check, rather than decrypting the vault twice.
func (s *Session) Search(name, pattern string) ([]SearchResult, error) {
	v, ident, err := s.Vault(name)
	if err != nil {
		return nil, err
	}
	held := s.vaults[v.Name]

	// buildingNow is only non-nil when ensureIndex just ran a fresh
	// decrypt pass to build the index for the first time — reusing it
	// below is what keeps that first search to one pass, not two.
	buildingNow, err := s.buildIndexIfMissing(held, ident)
	if err != nil {
		return nil, err
	}

	lowerPattern := strings.ToLower(pattern)
	matched := held.index.matchText(lowerPattern)

	bodyEntries := buildingNow
	if bodyEntries == nil {
		bodyEntries, err = held.vault.decryptAll(ident)
		if err != nil {
			return nil, err
		}
	}
	for id, e := range bodyEntries {
		if _, matchedOnText := matched[id]; matchedOnText {
			// The index already matched this entry on its title or
			// description, and that classification wins: MatchedBody
			// means the match came from the body *rather than* from
			// title/description, and one-shot's Vault.Search gives the
			// text stages the same precedence. Without this, an entry
			// whose title and value both contain the pattern would be
			// reported as a body match in a session and a title match
			// one-shot — the same entry and query, classified two ways
			// depending only on which mode asked.
			continue
		}
		if !matchesBody(e, lowerPattern) {
			continue
		}
		matched[id] = SearchResult{ID: id, Title: e.Title, Description: e.Description, MatchedBody: true}
	}

	out := make([]SearchResult, 0, len(matched))
	for _, r := range matched {
		out = append(out, r)
	}
	sortSearchResults(out)
	return out, nil
}

// Reindex forces a full rebuild of vault's ("" for the current one)
// index, decrypting every entry again regardless of whether one was
// already cached — `gage reindex`, for when entries/ changed from
// outside gage (a manual `git pull`) or the cache is merely suspected
// stale.
func (s *Session) Reindex(name string) error {
	v, ident, err := s.Vault(name)
	if err != nil {
		return err
	}
	held := s.vaults[v.Name]
	s.discardIndex(held)
	return s.ensureIndex(held, ident)
}

// NoteEntry updates vault's in-memory index, if it already has one, with
// e's current title/description/dates — the incremental half of M7's
// cache after insert/edit/rename. It does not itself write, encrypt, or
// commit anything: cmd/gage calls this right after the corresponding
// Vault.Insert/Update/Rename call has already done that, so Session's
// role stays cache bookkeeping rather than a second implementation of
// any CRUD verb (see TestSessionAddsNoDuplicateVaultMethods).
//
// A vault this session hasn't listed/resolved/searched yet has no index
// to update — a no-op, since the index that eventually gets built will
// read the already-current, post-write state and needs no reconciling.
func (s *Session) NoteEntry(vault string, id uuid.UUID, e Entry) {
	defer s.enter()()
	held := s.heldVaultFor(vault)
	if held == nil || held.index == nil {
		return
	}
	held.index.put(id, e)
	s.warnIndexLockOnce(held)
}

// ForgetEntry drops id from vault's index, if it has one — the
// incremental half of M7's cache after `rm`. Like NoteEntry, a no-op
// when there's no index yet to update.
func (s *Session) ForgetEntry(vault string, id uuid.UUID) {
	defer s.enter()()
	held := s.heldVaultFor(vault)
	if held == nil || held.index == nil {
		return
	}
	held.index.remove(id)
}

// EntryTitleCandidates returns the current vault's cached entry titles,
// for tab-completion of an entry-query argument (M13). It never unlocks:
// no current vault, or one this session hasn't unlocked, yields no
// candidates rather than a prompt. When the vault is already unlocked,
// it reuses ensureIndex's lazy build exactly like Resolve/List/Search do,
// so completing against an unlocked-but-unindexed vault triggers one
// decrypt pass, never one per keystroke.
//
// Only the *current* vault, never one named by --use elsewhere on the
// line and never another vault this session happens to have unlocked —
// see the M13 plan's "entry-title candidates: current vault only".
func (s *Session) EntryTitleCandidates() ([]string, error) {
	if s.current == "" {
		return nil, nil
	}
	held, ok := s.vaults[s.current]
	if !ok || held.ident == nil {
		return nil, nil
	}
	if err := s.ensureIndex(held, held.ident); err != nil {
		return nil, err
	}
	titles := held.index.titles()
	out := make([]string, 0, len(titles))
	for _, t := range titles {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// heldVaultFor resolves name ("" meaning the current vault) to this
// session's held record for it, or nil if that vault has never been
// used this session. Unlike Vault, it never opens or unlocks anything —
// callers only reach it once a vault is already known to be held.
func (s *Session) heldVaultFor(name string) *heldVault {
	if name == "" {
		name = s.current
	}
	return s.vaults[name]
}

// ensureIndex makes sure held has an index, building one from a full
// decrypt pass if this is the first ls/show/search/resolve for this
// vault in the session. A no-op, with no decryption, once an index
// already exists.
func (s *Session) ensureIndex(held *heldVault, ident *Identity) error {
	_, err := s.buildIndexIfMissing(held, ident)
	return err
}

// buildIndexIfMissing is ensureIndex's implementation, returning the
// freshly decrypted entries when it actually built an index — nil when
// one already existed and nothing was decrypted — so Search can reuse
// that same pass for its body-text check instead of decrypting the vault
// a second time on a session's very first search.
func (s *Session) buildIndexIfMissing(held *heldVault, ident *Identity) (map[uuid.UUID]Entry, error) {
	if held.index != nil {
		return nil, nil
	}
	entries, err := held.vault.decryptAll(ident)
	if err != nil {
		return nil, err
	}
	idx := newIndex(held.vault.locker, ident.PageLocked())
	for id, e := range entries {
		idx.put(id, e)
	}
	held.index = idx
	s.warnIndexLockOnce(held)
	return entries, nil
}

// warnIndexLockOnce surfaces held's index page-lock failure through the
// Prompter exactly once per unlock — never at all if the vault's own
// Identity already failed and warned about the same underlying
// constraint (Index.lockOK folds that check in: see its doc comment).
func (s *Session) warnIndexLockOnce(held *heldVault) {
	if held.index == nil || held.indexWarned || held.index.lockOK() {
		return
	}
	held.indexWarned = true
	if s.prompter != nil {
		s.prompter.Warn(fmt.Sprintf(
			"gage: could not lock %s's metadata index into memory; continuing without it, "+
				"so cached titles/descriptions may be written to swap.", held.vault.Name))
	}
}

// isCandidate reports whether id was one of the choices actually offered.
func isCandidate(list CandidateList, id string) bool {
	for _, c := range list.Candidates {
		if c.ID == id {
			return true
		}
	}
	return false
}

// Close drops every key this session holds. It's what the REPL calls on
// exit/EOF, and it's the same per-vault path Lock takes rather than a
// parallel one. Closing twice is safe.
func (s *Session) Close() error {
	names := make([]string, 0, len(s.vaults))
	for name := range s.vaults {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		if err := s.lockHeld(s.vaults[name]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
