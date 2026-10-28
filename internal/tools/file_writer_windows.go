//go:build windows

package tools

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	maxWriteAttempts = 5
	initialBackoff   = 2 * time.Millisecond
	maxBackoff       = 20 * time.Millisecond
)

// writeOpFunc is the signature for the underlying write operation, exposed for testing.
type writeOpFunc func(path string, data []byte, perm os.FileMode) error

// sleepFunc is the signature for the backoff sleep, exposed for testing.
type sleepFunc func(d time.Duration)

var (
	// defaultWriteOp is the actual filesystem write operation.
	defaultWriteOp writeOpFunc = os.WriteFile

	// defaultSleep is time.Sleep.
	defaultSleep sleepFunc = time.Sleep
)

// writeToolFile writes data to path with perm. On Windows, if a sharing violation
// (ERROR_SHARING_VIOLATION) occurs due to transient filesystem locks, it retries
// with a bounded exponential backoff up to 5 attempts (total wait ~30-37ms).
// Any other error returns immediately without retry.
func writeToolFile(path string, data []byte, perm os.FileMode) error {
	return writeToolFileWithRetry(path, data, perm, defaultWriteOp, defaultSleep)
}

func writeToolFileWithRetry(path string, data []byte, perm os.FileMode, writeOp writeOpFunc, sleep sleepFunc) error {
	var err error
	backoff := initialBackoff

	for attempt := range maxWriteAttempts {
		err = writeOp(path, data, perm)
		if err == nil {
			return nil
		}

		if !isSharingViolation(err) {
			return err
		}

		if attempt < maxWriteAttempts-1 {
			if sleep != nil {
				sleep(backoff)
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}

	return err
}

func isSharingViolation(err error) bool {
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == windows.ERROR_SHARING_VIOLATION {
		return true
	}
	return false
}
