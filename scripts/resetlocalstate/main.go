// Command resetlocalstate deletes every directory gage owns on this
// machine — $GAGE_CONFIG, $GAGE_DATA, and $GAGE_STATE — so a developer can
// re-test the tool from a fresh-install state. It is a `make` convenience,
// not something shipped or reachable from the gage binary: destroying a
// user's vaults and identities has no legitimate place in the CLI's own
// command surface.
//
// It refuses to run unannounced: it prints exactly what it resolved and
// requires an interactive "yes" before deleting anything.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/denmark/gage/internal/gage/xdgpaths"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "resetlocalstate:", err)
		os.Exit(1)
	}
}

func run() error {
	configDir, err := xdgpaths.ConfigDir()
	if err != nil {
		return fmt.Errorf("resolving $GAGE_CONFIG: %w", err)
	}
	dataDir, err := xdgpaths.DataDir()
	if err != nil {
		return fmt.Errorf("resolving $GAGE_DATA: %w", err)
	}
	stateDir, err := xdgpaths.StateDir()
	if err != nil {
		return fmt.Errorf("resolving $GAGE_STATE: %w", err)
	}
	dirs := []string{configDir, dataDir, stateDir}

	fmt.Println("This will permanently delete ALL local gage state on this machine:")
	fmt.Println()
	fmt.Printf("  $GAGE_CONFIG  %s   (global config: registered vaults, defaults)\n", configDir)
	fmt.Printf("  $GAGE_DATA    %s   (every vault's git checkout + identity files)\n", dataDir)
	fmt.Printf("  $GAGE_STATE   %s   (locks, trust cache, remote auth tokens, history)\n", stateDir)
	fmt.Println()
	fmt.Println("Any vault whose only local identity lives here becomes unrecoverable")
	fmt.Println("on this device unless its recipients or a synced remote still exist.")
	fmt.Println()
	fmt.Print(`Type "yes" to permanently delete all of the above: `)

	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		fmt.Println("aborted, nothing deleted")
		return nil
	}

	for _, dir := range dirs {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing %s: %w", dir, err)
		}
	}
	fmt.Println("done: all local gage state deleted")
	return nil
}
