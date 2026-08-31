package xdgpaths

import (
	"path/filepath"
	"testing"
)

func env(m map[string]string) Env {
	return func(key string) string { return m[key] }
}

func TestUnixDefaults(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			home := "/home/testuser"
			e := env(nil)

			cfg, err := resolve(goos, e, home, Config)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(home, ".config", "gage"); cfg != want {
				t.Errorf("Config = %q, want %q", cfg, want)
			}

			data, err := resolve(goos, e, home, Data)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(home, ".local", "share", "gage"); data != want {
				t.Errorf("Data = %q, want %q", data, want)
			}

			state, err := resolve(goos, e, home, State)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(home, ".local", "state", "gage"); state != want {
				t.Errorf("State = %q, want %q", state, want)
			}
		})
	}
}

func TestUnixXDGEnvOverride(t *testing.T) {
	home := "/home/testuser"
	e := env(map[string]string{
		"XDG_CONFIG_HOME": "/custom/config",
		"XDG_DATA_HOME":   "/custom/data",
		"XDG_STATE_HOME":  "/custom/state",
	})

	cfg, err := resolve("linux", e, home, Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/config", "gage"); cfg != want {
		t.Errorf("Config = %q, want %q", cfg, want)
	}

	data, err := resolve("linux", e, home, Data)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/data", "gage"); data != want {
		t.Errorf("Data = %q, want %q", data, want)
	}

	state, err := resolve("darwin", e, home, State)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/state", "gage"); state != want {
		t.Errorf("State = %q, want %q", state, want)
	}
}

func TestWindowsDefaults(t *testing.T) {
	home := `C:\Users\testuser`
	e := env(map[string]string{
		"APPDATA":      `C:\Users\testuser\AppData\Roaming`,
		"LOCALAPPDATA": `C:\Users\testuser\AppData\Local`,
	})

	cfg, err := resolve("windows", e, home, Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`C:\Users\testuser\AppData\Roaming`, "gage"); cfg != want {
		t.Errorf("Config = %q, want %q", cfg, want)
	}

	data, err := resolve("windows", e, home, Data)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`C:\Users\testuser\AppData\Local`, "gage"); data != want {
		t.Errorf("Data = %q, want %q", data, want)
	}

	state, err := resolve("windows", e, home, State)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`C:\Users\testuser\AppData\Local`, "gage", "state"); state != want {
		t.Errorf("State = %q, want %q", state, want)
	}
}

func TestWindowsMissingEnvFallsBackToHome(t *testing.T) {
	home := `C:\Users\testuser`
	e := env(nil)

	cfg, err := resolve("windows", e, home, Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "AppData", "Roaming", "gage"); cfg != want {
		t.Errorf("Config = %q, want %q", cfg, want)
	}

	data, err := resolve("windows", e, home, Data)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "AppData", "Local", "gage"); data != want {
		t.Errorf("Data = %q, want %q", data, want)
	}
}

func TestWindowsXDGEnvOverride(t *testing.T) {
	home := `C:\Users\testuser`
	e := env(map[string]string{"XDG_CONFIG_HOME": `D:\dotfiles\config`})

	cfg, err := resolve("windows", e, home, Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(`D:\dotfiles\config`, "gage"); cfg != want {
		t.Errorf("Config = %q, want %q", cfg, want)
	}
}

func TestEmptyHomeDirIsAnError(t *testing.T) {
	if _, err := resolve("linux", env(nil), "", Config); err == nil {
		t.Fatal("expected an error for an empty home directory, got nil")
	}
}

func TestRealAccessorsResolve(t *testing.T) {
	// Smoke test: the real, current-process accessors should resolve
	// without error on whatever platform runs the test suite.
	if _, err := ConfigDir(); err != nil {
		t.Errorf("ConfigDir: %v", err)
	}
	if _, err := DataDir(); err != nil {
		t.Errorf("DataDir: %v", err)
	}
	if _, err := StateDir(); err != nil {
		t.Errorf("StateDir: %v", err)
	}
}
