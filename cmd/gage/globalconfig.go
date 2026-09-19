package main

import (
	"os"
	"path/filepath"

	"github.com/distillerylabs/gage/internal/gage/config"
	"github.com/distillerylabs/gage/internal/gage/xdgpaths"
)

// globalConfigPath resolves $GAGE_CONFIG/config.toml.
func globalConfigPath() (string, error) {
	dir, err := xdgpaths.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// readGlobalConfig reads the global config, treating "file doesn't
// exist yet" (no vault has ever been registered) as an empty registry
// rather than an error.
func readGlobalConfig() (config.Global, error) {
	path, err := globalConfigPath()
	if err != nil {
		return config.Global{}, err
	}
	g, err := config.Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config.Global{}, nil
		}
		return config.Global{}, err
	}
	return g, nil
}

// writeGlobalConfig atomically writes g back, creating $GAGE_CONFIG
// first if this is the first vault ever registered on this machine.
func writeGlobalConfig(g config.Global) error {
	path, err := globalConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.Write(path, g)
}
