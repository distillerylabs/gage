package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/denmark/gage/internal/gage"
	"github.com/denmark/gage/internal/gage/exitcode"
	"github.com/denmark/gage/internal/gage/gitrepo"
	"github.com/denmark/gage/internal/gage/vaultconfig"
)

// newVaultCommand groups the vault-generic lifecycle commands that
// don't create a vault (that's init/clone) but list, inspect, forget, or
// re-point one already registered. See "Vault lifecycle" in the design
// doc.
func newVaultCommand(app *App) *cobra.Command {
	parent := &cobra.Command{
		Use:   "vault",
		Short: "Manage registered vaults",
	}
	parent.AddCommand(newVaultListCommand(app))
	parent.AddCommand(newVaultInfoCommand(app))
	parent.AddCommand(newVaultRemoveCommand(app))
	parent.AddCommand(newVaultSetDefaultCommand(app))
	return parent
}

func newVaultListCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: commandShort("vault list"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			names := make([]string, 0, len(g.Vaults))
			for name := range g.Vaults {
				names = append(names, name)
			}
			sort.Strings(names)

			lines := make([]string, 0, len(names))
			for _, name := range names {
				marker := "  "
				if name == g.Current {
					marker = "* "
				}
				lines = append(lines, marker+name)
			}
			writeOut(app.Out, lines)
			return nil
		},
	}
}

func newVaultInfoCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info [name]",
		Short: commandShort("vault info"),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}

			name := g.Current
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" {
				return exitcode.New(exitcode.Usage, "gage: no vault name given and no current vault is set")
			}

			entry, ok := g.Vaults[name]
			if !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}

			vc, err := vaultconfig.Read(filepath.Join(entry.Path, ".gage", "config.toml"))
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}

			lines := []string{
				fmt.Sprintf("name: %s", name),
				fmt.Sprintf("type: %s", vc.Vault.Type),
				fmt.Sprintf("method: %s", vc.Method.Default),
				fmt.Sprintf("recipients: %d", len(vc.Recipients)),
			}

			// Git-type-specific detail — see "Git-specific commands":
			// remote and clean/dirty state have no meaning for a future
			// non-git vault type.
			if vc.Vault.Type == gage.TypeGit {
				remote, err := gitrepo.RemoteURL(entry.Path)
				if err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				if remote == "" {
					remote = "(none)"
				}
				lines = append(lines, fmt.Sprintf("remote: %s", remote))

				clean, err := gitrepo.IsClean(entry.Path)
				if err != nil {
					return exitcode.Wrap(exitcode.Internal, err)
				}
				status := "dirty"
				if clean {
					status = "clean"
				}
				lines = append(lines, fmt.Sprintf("status: %s", status))
			}

			writeOut(app.Out, lines)
			return nil
		},
	}
}

func newVaultRemoveCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: commandShort("vault remove"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			if _, ok := g.Vaults[name]; !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}

			// Forgets the registration only — the underlying git repo
			// and its files are never touched. See "Vault lifecycle".
			delete(g.Vaults, name)
			if g.Current == name {
				g.Current = ""
			}
			if err := writeGlobalConfig(g); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			return nil
		},
	}
}

func newVaultSetDefaultCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "set-default <name>",
		Short: commandShort("vault set-default"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			g, err := readGlobalConfig()
			if err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			if _, ok := g.Vaults[name]; !ok {
				return exitcode.Newf(exitcode.NotFound, "gage: no such vault %q", name)
			}
			g.Current = name
			if err := writeGlobalConfig(g); err != nil {
				return exitcode.Wrap(exitcode.Internal, err)
			}
			return nil
		},
	}
}
