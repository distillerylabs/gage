package main

// Availability says which of the two invocation modes — one-shot
// (`gage <cmd>`) and session (the `gage>` REPL) — a command exists in.
// Per the design doc, every command is available in both except init and
// clone (one-shot only, since they create a vault rather than operating
// on one) and the session's own meta-verbs (session-only, since they
// don't exist outside a session at all). See Q-HELP-SURFACES and
// Q-CMD-AVAILABILITY.
type Availability int

const (
	// availUnset is the zero value, and it is deliberately not a valid
	// availability. If AvailBoth were zero, a registry entry that simply
	// forgot the field would silently become "available everywhere" —
	// failing open, and in the one direction that matters: a session-only
	// command would appear in gage --help, which is precisely what
	// Q-HELP-SURFACES says must never happen. An unset field is instead
	// invalid and caught by TestEveryRegistryEntryHasShortAndGroup.
	availUnset Availability = iota
	AvailBoth
	AvailOneShotOnly
	AvailSessionOnly
)

// Valid reports whether the availability was explicitly set to one of
// the three real values.
func (a Availability) Valid() bool {
	return a == AvailBoth || a == AvailOneShotOnly || a == AvailSessionOnly
}

// OneShotVisible reports whether a command belongs in gage --help/gage
// help's rendered set.
func (a Availability) OneShotVisible() bool {
	return a == AvailBoth || a == AvailOneShotOnly
}

// SessionVisible reports whether a command belongs in in-session help's
// rendered set — and, since the two surfaces render from this one
// predicate pair, whether the REPL should accept it at all. Everything
// except init/clone, per Q-CMD-AVAILABILITY.
func (a Availability) SessionVisible() bool {
	return a == AvailBoth || a == AvailSessionOnly
}

// CommandInfo is one command's entry in the registry — name, aliases,
// description, group, and where it's available. Per Q-HELP-SURFACES this
// is the single source of truth for both help surfaces: a command can't
// appear on one and go missing from the other, because both render from
// this table rather than two hand-maintained lists.
//
// Only the one-shot surface reads it today (see oneShotCommands); the
// session surface is M6's, and the filter it needs lands with the code
// that renders it. What M0 fixes is the Availability tagging every later
// milestone depends on, not a reader for a surface that doesn't exist.
type CommandInfo struct {
	Name    string
	Aliases []string
	Short   string
	Group   string

	// Usage is the argument syntax rendered after the name — "<vault>"
	// for use, "[vault]" for lock. It exists for the session-only
	// meta-verbs, whose Cobra commands are generated from this table
	// (see newStubCommand) rather than hand-written: recording the
	// syntax here is what lets in-session help present vault selection
	// as the bare `use <vault>` (Q-HELP-SURFACES) without a second list
	// to keep in step. Empty for commands that take no arguments, and
	// for the vault-domain commands whose own Cobra definition already
	// carries their use line.
	Usage string

	Availability Availability
}

// UsageName renders a command the way both help surfaces list it: the
// canonical name plus its argument syntax, if it declares any.
func (ci CommandInfo) UsageName() string {
	if ci.Usage == "" {
		return ci.Name
	}
	return ci.Name + " " + ci.Usage
}

// Groups mirror the design doc's own command-reference sections, in the
// order they appear there — see "Command reference" in the design doc.
const (
	GroupSession   = "Session"
	GroupVault     = "Vault lifecycle"
	GroupIdentity  = "Identity"
	GroupRecipient = "Recipients"
	GroupEntry     = "Entry CRUD"
	GroupSync      = "Sync"
	GroupGit       = "Git-specific"
)

// groupOrder fixes the display order groups render in; groups not listed
// here sort after these, alphabetically, so a future group added without
// updating this list still renders (deterministically) rather than being
// silently dropped.
var groupOrder = []string{
	GroupSession,
	GroupVault,
	GroupIdentity,
	GroupRecipient,
	GroupEntry,
	GroupSync,
	GroupGit,
}

// registry is gage's single command table. Every milestone that adds a
// command registers it here — enforced by TestRegistryCompleteness, which
// walks the real Cobra command tree and fails if the two ever drift.
//
// M0 only implements the session's own meta-verbs (as real, hidden
// one-shot-mode stubs — see session.go) since every vault-domain command
// (vault/identity/recipient/entry-crud/sync/git) is a later milestone's
// work. That's expected, not a gap: see the M0 plan's "Definition of
// done" — gage --help legitimately has nothing to show yet beyond the
// mechanism this registry proves out.
var registry = []CommandInfo{
	{
		Name:         "use",
		Usage:        "<vault>",
		Short:        "Switch/unlock the active vault for this session",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		Name:         "lock",
		Usage:        "[vault]",
		Short:        "Drop key material for one vault (or all) without exiting",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		Name:         "status",
		Aliases:      []string{"whoami"},
		Short:        "List vaults touched this session and their lock state",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		Name:         "exit",
		Aliases:      []string{"quit"},
		Short:        "Leave the session",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		// help is session-only *for listing purposes*, which is the only
		// thing Availability governs. `gage help` itself works one-shot
		// and always has (installHelp wires it); what the design doc
		// rules out is gage --help *listing* it alongside the
		// vault-domain commands, exactly as for the other meta-verbs.
		// See "Help".
		Name:         "help",
		Usage:        "[command]",
		Short:        "Show help for gage's commands",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		Name:         "init",
		Short:        "Create a new vault",
		Group:        GroupVault,
		Availability: AvailOneShotOnly,
	},
	{
		// One-shot only, like init, and for the same reason: it creates a
		// vault rather than operating on one, which leaves "does the new
		// vault become the session's current vault?" unanswered. See
		// Q-CMD-AVAILABILITY.
		Name:         "clone",
		Short:        "Clone an existing vault from a remote",
		Group:        GroupVault,
		Availability: AvailOneShotOnly,
	},
	{
		Name:         "vault list",
		Short:        "List registered vaults",
		Group:        GroupVault,
		Availability: AvailBoth,
	},
	{
		Name:         "vault info",
		Short:        "Show a vault's type, method, recipient count, and (for git) remote/status",
		Group:        GroupVault,
		Availability: AvailBoth,
	},
	{
		Name:         "vault remove",
		Short:        "Forget a vault locally, leaving its files untouched",
		Group:        GroupVault,
		Availability: AvailBoth,
	},
	{
		Name:         "vault set-default",
		Short:        "Change which vault is used when none is given",
		Group:        GroupVault,
		Availability: AvailBoth,
	},
	{
		Name:         "sync",
		Short:        "Pull, merge what can be merged, then push",
		Group:        GroupSync,
		Availability: AvailBoth,
	},
	{
		Name:         "pull",
		Short:        "Fast-forward this vault from its remote",
		Group:        GroupSync,
		Availability: AvailBoth,
	},
	{
		Name:         "push",
		Short:        "Publish this vault's commits to its remote",
		Group:        GroupSync,
		Availability: AvailBoth,
	},
	{
		Name:         "git set-remote",
		Short:        "Set or change a vault's git remote (origin)",
		Group:        GroupGit,
		Availability: AvailBoth,
	},
	{
		Name:         "auth login",
		Short:        "Store a token for a git host",
		Group:        GroupGit,
		Availability: AvailBoth,
	},
	{
		Name:         "auth status",
		Short:        "Show which git hosts have a token",
		Group:        GroupGit,
		Availability: AvailBoth,
	},
	{
		Name:         "auth logout",
		Short:        "Forget a git host's token",
		Group:        GroupGit,
		Availability: AvailBoth,
	},
	{
		Name:         "insert",
		Short:        "Create a new entry",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "show",
		Short:        "Print an entry's value",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "cat",
		Short:        "Print an entry's full decrypted contents",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "edit",
		Short:        "Edit an entry's full YAML in $EDITOR",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "rename",
		Short:        "Change an entry's title",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "generate",
		Short:        "Create a new entry with a randomly generated value",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "rm",
		Short:        "Delete an entry",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "ls",
		Short:        "List entries with their ids, dates, and last writer",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "search",
		Aliases:      []string{"grep"},
		Short:        "Find entries by title, description, or body text",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
	{
		Name:         "reindex",
		Short:        "Force a session's cached metadata index to rebuild",
		Group:        GroupEntry,
		Availability: AvailBoth,
	},
}

// commandShort looks up a registry entry's Short description by its
// (possibly space-joined, for a nested command) name. It exists so a
// dedicated command builder — anything beyond the generic
// newStubCommand — still sources its help text from the registry
// rather than duplicating it, keeping the registry the single source of
// truth for both help surfaces (see CommandInfo's doc comment). Called
// only with names this file itself defines above, so a missing entry is
// a programming error, not a runtime condition to recover from.
func commandShort(name string) string {
	ci, ok := findCommand(name)
	if !ok {
		panic("registry: no entry for " + name)
	}
	return ci.Short
}

// oneShotCommands returns the registry entries gage --help/gage help
// render, in registry order.
func oneShotCommands() []CommandInfo {
	return filterCommands(Availability.OneShotVisible)
}

// sessionCommands returns the registry entries in-session help renders —
// the session-only meta-verbs plus every vault-domain command available
// in a session. Same table, different filter: that is the whole
// mechanism behind "a command can't appear in one surface and go missing
// from the other" (Q-HELP-SURFACES).
func sessionCommands() []CommandInfo {
	return filterCommands(Availability.SessionVisible)
}

func filterCommands(visible func(Availability) bool) []CommandInfo {
	var out []CommandInfo
	for _, ci := range registry {
		if visible(ci.Availability) {
			out = append(out, ci)
		}
	}
	return out
}

// findCommand looks up a registry entry by its canonical name or any of
// its aliases.
func findCommand(name string) (CommandInfo, bool) {
	for _, ci := range registry {
		if ci.Name == name {
			return ci, true
		}
		for _, a := range ci.Aliases {
			if a == name {
				return ci, true
			}
		}
	}
	return CommandInfo{}, false
}

// commandGroup is one section of a rendered help listing.
type commandGroup struct {
	Group    string
	Commands []CommandInfo
}

// groupedOneShotCommands buckets oneShotCommands by Group, in
// groupOrder, for the help renderer.
func groupedOneShotCommands() []commandGroup { return groupCommands(oneShotCommands()) }

// groupedSessionCommands is the same bucketing for in-session help.
func groupedSessionCommands() []commandGroup { return groupCommands(sessionCommands()) }

func groupCommands(cmds []CommandInfo) []commandGroup {
	byGroup := map[string][]CommandInfo{}
	for _, ci := range cmds {
		byGroup[ci.Group] = append(byGroup[ci.Group], ci)
	}

	seen := map[string]bool{}
	var out []commandGroup
	for _, g := range groupOrder {
		if cmds, ok := byGroup[g]; ok {
			out = append(out, commandGroup{g, cmds})
			seen[g] = true
		}
	}
	// Any group not in groupOrder still renders, just after the known
	// ones, so a forgotten groupOrder update doesn't hide a command.
	for g, cmds := range byGroup {
		if !seen[g] {
			out = append(out, commandGroup{g, cmds})
		}
	}
	return out
}
