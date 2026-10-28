//go:build !windows

package tools

import "os"

// writeToolFile writes data to path with perm.
// On non-Windows platforms, it delegates directly to os.WriteFile without retries.
func writeToolFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
