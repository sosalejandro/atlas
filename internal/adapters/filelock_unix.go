//go:build !windows

package adapters

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on f.
//
// The lock guards the .tmp file a write goes through before its atomic rename,
// so two processes writing the same target path cannot interleave into one
// temp file and produce a half-encoded YAML document that the rename then
// publishes as if it were complete.
func lockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", f.Name(), err)
	}
	return nil
}

// unlockFile releases the lock taken by lockFile. Closing the descriptor
// releases it too, so the error is advisory -- callers unlock on the failure
// path where they are already returning a more useful error.
func unlockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock %s: %w", f.Name(), err)
	}
	return nil
}
