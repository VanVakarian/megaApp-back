// Package appendfile provides the durable append primitive shared by every rotating NDJSON/log
// file writer in this backend (internal/platform/rotatingfile, internal/metrics) — open-or-create, append,
// fsync, close. Rotation policy (when to roll over, numbered vs timestamped names, archiving)
// stays with each caller; only the physical write is shared.
package appendfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Append durably appends data to the file at path, creating it and any missing parent
// directories if necessary. The write is fsynced before the file is closed, so a crash right
// after Append returns nil can never lose the appended bytes.
func Append(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(fmt.Errorf("append to %s: %w", path, err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync %s: %w", path, err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
