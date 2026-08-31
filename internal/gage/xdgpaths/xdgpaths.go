// Package xdgpaths resolves the three directory roots gage owns: config,
// data, and state. On Linux and macOS these follow the XDG Base Directory
// spec; on Windows they map onto the per-user directories Windows already
// provides. See the design doc's "Global config" section.
package xdgpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Role names one of the three directory roots gage owns.
type Role int

const (
	Config Role = iota
	Data
	State
)

// appDirName is the subdirectory every gage-owned path lives under, inside
// whichever role root is resolved.
const appDirName = "gage"

// Env is the subset of the process environment path resolution needs.
// Real callers pass os.Getenv; tests pass a fake map lookup so both the
// Windows and Unix branches are exercised regardless of the host running
// the test.
type Env func(key string) string

// ConfigDir, DataDir, and StateDir resolve the real, current-process roots:
// GOOS from runtime.GOOS, environment from os.Getenv, and home directory
// from os.UserHomeDir. Each returns "<role root>/gage".
func ConfigDir() (string, error) { return realResolve(Config) }
func DataDir() (string, error)   { return realResolve(Data) }
func StateDir() (string, error)  { return realResolve(State) }

func realResolve(role Role) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("xdgpaths: resolving home directory: %w", err)
	}
	return resolve(runtime.GOOS, os.Getenv, home, role)
}

// resolve is the pure core of path resolution: given a GOOS value, an
// environment lookup, and a home directory, compute the role root. Kept
// independent of the real runtime.GOOS/os.Getenv so both the Windows and
// Unix branches can be unit-tested on any host, not just their native one.
func resolve(goos string, env Env, homeDir string, role Role) (string, error) {
	if homeDir == "" {
		return "", fmt.Errorf("xdgpaths: home directory is empty")
	}

	// An explicit XDG_* env var wins on any OS, Windows included — see
	// the design doc's "Global config" table.
	if v := env(xdgVar(role)); v != "" {
		return filepath.Join(v, appDirName), nil
	}

	if goos == "windows" {
		return windowsDefault(env, homeDir, role)
	}
	return unixDefault(homeDir, role), nil
}

func xdgVar(role Role) string {
	switch role {
	case Config:
		return "XDG_CONFIG_HOME"
	case Data:
		return "XDG_DATA_HOME"
	case State:
		return "XDG_STATE_HOME"
	default:
		panic("xdgpaths: unknown role")
	}
}

func unixDefault(homeDir string, role Role) string {
	switch role {
	case Config:
		return filepath.Join(homeDir, ".config", appDirName)
	case Data:
		return filepath.Join(homeDir, ".local", "share", appDirName)
	case State:
		return filepath.Join(homeDir, ".local", "state", appDirName)
	default:
		panic("xdgpaths: unknown role")
	}
}

func windowsDefault(env Env, homeDir string, role Role) (string, error) {
	appData := env("APPDATA")
	if appData == "" {
		appData = filepath.Join(homeDir, "AppData", "Roaming")
	}
	localAppData := env("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(homeDir, "AppData", "Local")
	}

	switch role {
	case Config:
		return filepath.Join(appData, appDirName), nil
	case Data:
		return filepath.Join(localAppData, appDirName), nil
	case State:
		return filepath.Join(localAppData, appDirName, "state"), nil
	default:
		panic("xdgpaths: unknown role")
	}
}
