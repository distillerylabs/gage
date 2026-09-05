package gage

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/denmark/gage/internal/gage/exitcode"
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
	if _, _, err := s.Vault(name); err != nil {
		return err
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

	held, ok := s.vaults[name]
	if !ok {
		v, err := s.open(name)
		if err != nil {
			return nil, nil, err
		}
		held = &heldVault{vault: v}
		s.vaults[name] = held
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
// that happen to agree today.
func (s *Session) lockHeld(held *heldVault) error {
	if held.ident == nil {
		return nil
	}
	ident := held.ident
	held.ident = nil
	return ident.Close()
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
func (s *Session) Resolve(name, query string) (uuid.UUID, Entry, error) {
	v, ident, err := s.Vault(name)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}

	id, e, err := v.Resolve(query, ident)
	if err == nil {
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

	e, err = v.ReadEntry(chosenID, ident)
	if err != nil {
		return uuid.Nil, Entry{}, err
	}
	return chosenID, e, nil
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
