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
	AvailBoth Availability = iota
	AvailOneShotOnly
	AvailSessionOnly
)

// OneShotVisible reports whether a command belongs in gage --help/gage
// help's rendered set.
func (a Availability) OneShotVisible() bool {
	return a == AvailBoth || a == AvailOneShotOnly
}

// SessionVisible reports whether a command belongs in the in-session
// help's rendered set (M6).
func (a Availability) SessionVisible() bool {
	return a == AvailBoth || a == AvailSessionOnly
}

// CommandInfo is one command's entry in the registry that both help
// surfaces render from — name, aliases, description, group, and where
// it's available. Per Q-HELP-SURFACES, this is the single source of
// truth: a command can't appear on one help surface and go missing from
// the other, because both render from this table rather than two
// hand-maintained lists.
type CommandInfo struct {
	Name         string
	Aliases      []string
	Short        string
	Group        string
	Availability Availability
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
		Short:        "Switch/unlock the active vault for this session",
		Group:        GroupSession,
		Availability: AvailSessionOnly,
	},
	{
		Name:         "lock",
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
}

// oneShotCommands returns the registry entries gage --help/gage help
// render, in registry order.
func oneShotCommands() []CommandInfo {
	var out []CommandInfo
	for _, ci := range registry {
		if ci.Availability.OneShotVisible() {
			out = append(out, ci)
		}
	}
	return out
}

// sessionVisibleCommands returns the registry entries in-session help
// (M6) renders.
func sessionVisibleCommands() []CommandInfo {
	var out []CommandInfo
	for _, ci := range registry {
		if ci.Availability.SessionVisible() {
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

// groupedOneShotCommands buckets oneShotCommands by Group, in
// groupOrder, for the help renderer.
func groupedOneShotCommands() []struct {
	Group    string
	Commands []CommandInfo
} {
	byGroup := map[string][]CommandInfo{}
	for _, ci := range oneShotCommands() {
		byGroup[ci.Group] = append(byGroup[ci.Group], ci)
	}

	seen := map[string]bool{}
	var out []struct {
		Group    string
		Commands []CommandInfo
	}
	for _, g := range groupOrder {
		if cmds, ok := byGroup[g]; ok {
			out = append(out, struct {
				Group    string
				Commands []CommandInfo
			}{g, cmds})
			seen[g] = true
		}
	}
	// Any group not in groupOrder still renders, just after the known
	// ones, so a forgotten groupOrder update doesn't hide a command.
	for g, cmds := range byGroup {
		if !seen[g] {
			out = append(out, struct {
				Group    string
				Commands []CommandInfo
			}{g, cmds})
		}
	}
	return out
}
