package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// fileSnapshot records the on-disk state a preview diff was computed from, so
// execution can refuse to apply a change the user never saw approved.
type fileSnapshot struct {
	resolvedPath string
	oldExists    bool
	oldHash      string // hex sha256 of the file content observed at preview time
}

// fileSnapshotStore holds preview snapshots keyed by request. Calls are
// sequential per approval in practice, but the mutex keeps concurrent use safe.
type fileSnapshotStore struct {
	mu    sync.Mutex
	items map[string]fileSnapshot
}

func (s *fileSnapshotStore) put(key string, snap fileSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = make(map[string]fileSnapshot)
	}
	s.items[key] = snap
}

// get returns the snapshot for key without consuming it. Keeping it on refusal
// means a retried execution still cannot bypass the approval-time state.
func (s *fileSnapshotStore) get(key string) (fileSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.items[key]
	return snap, ok
}

// remove discards the snapshot after a successful write.
func (s *fileSnapshotStore) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
}

func hashFileContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// snapshotFromRead records the state observed by a caller's ReadFile.
func snapshotFromRead(path string, data []byte, readErr error) (fileSnapshot, error) {
	if readErr != nil {
		// Matches the diff behaviour: an unreadable or absent file has no old content.
		resolved, err := resolveExistingAncestor(path)
		if err != nil {
			return fileSnapshot{}, err
		}
		return fileSnapshot{resolvedPath: resolved}, nil
	}
	return snapshotForContent(path, data)
}

// snapshotForContent records an existing file whose content the caller already read.
func snapshotForContent(path string, data []byte) (fileSnapshot, error) {
	resolved, err := resolveExistingAncestor(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{
		resolvedPath: resolved,
		oldExists:    true,
		oldHash:      hashFileContent(data),
	}, nil
}

// matches reports whether the current state is identical to the snapshot.
// resolvedPath is compared case-folded on Windows, where the drive-letter case
// returned by the OS may vary between calls.
func (s fileSnapshot) matches(current fileSnapshot) bool {
	return normalizeForComparison(s.resolvedPath) == normalizeForComparison(current.resolvedPath) &&
		s.oldExists == current.oldExists &&
		s.oldHash == current.oldHash
}

func fileChangedSinceApprovalError(path string) string {
	return fmt.Sprintf("%s: file changed since approval — re-approval required", path)
}
