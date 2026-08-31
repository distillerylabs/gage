// Package atomicfile writes a file's full contents so that a reader never
// observes a partial write: the new content lands in a temp file in the
// same directory, is fsynced, and is only made visible via a rename. Used
// by every config writer in gage — global config, a vault's own
// .gage/config.toml, and the trust cache's known-config.toml.
package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile atomically replaces path's contents with data. If path doesn't
// exist yet, it's created with perm. An interrupted write (crash, kill)
// leaves either the old content or the new content in place — never a
// truncated mix of both, because the destination path is never opened for
// writing directly.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomicfile: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// If anything below fails, don't leave the temp file behind.
	success := false
	defer func() {
		if !success {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicfile: syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: closing temp file: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("atomicfile: setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomicfile: renaming into place: %w", err)
	}
	success = true
	return nil
}
