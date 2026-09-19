// Package recipients reads and writes a vault's .age-recipients file:
// one age public key per line, with no gage-specific framing, so a stock
// age/passage CLI can use it directly (age -R .age-recipients -e file).
// See "On-disk layout" in the design doc.
package recipients

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/distillerylabs/gage/internal/gage/atomicfile"
)

// Read parses path as one public key per line. Blank lines are skipped;
// nothing else about the format is gage-specific.
func Read(path string) ([]string, error) {
	// #nosec G304 -- path is a vault-relative location the caller
	// resolves (typically <vault>/.age-recipients), not attacker input.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var keys []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		keys = append(keys, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("recipients: reading %s: %w", path, err)
	}
	return keys, nil
}

// Write atomically writes keys to path, one per line, in the given
// order — no framing, no comments, so the file stays directly usable by
// a stock age/passage CLI.
func Write(path string, keys []string) error {
	var buf bytes.Buffer
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteByte('\n')
	}
	if err := atomicfile.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("recipients: writing %s: %w", path, err)
	}
	return nil
}
