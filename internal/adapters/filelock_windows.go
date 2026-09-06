//go:build windows

package adapters

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on f.
//
// Windows has no flock(2). LockFileEx is the equivalent primitive, and unlike
// flock it is MANDATORY rather than advisory -- a second process is refused the
// write outright rather than trusted to ask. That is a stronger guarantee than
// the unix path gives, not a weaker one, so the two are safe to treat as the
// same contract at the call site.
//
// The range is the whole file, expressed as the maximum 64-bit offset. Locking
// a byte range rather than a handle is how Windows models this; passing the
// full range is what makes it equivalent to LOCK_EX.
func lockFile(f *os.File) error {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		^uint32(0), ^uint32(0),
		&ol,
	)
	if err != nil {
		return fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return nil
}

// unlockFile releases the lock taken by lockFile.
func unlockFile(f *os.File) error {
	var ol windows.Overlapped
	err := windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		^uint32(0), ^uint32(0),
		&ol,
	)
	if err != nil {
		return fmt.Errorf("unlock %s: %w", f.Name(), err)
	}
	return nil
}
