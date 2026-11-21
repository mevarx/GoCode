package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mevarx/GoCode/internal/config"
)

// promptHistoryLimit bounds the recall list; a session that submits thousands
// of prompts should not grow without bound.
const promptHistoryLimit = 200

// promptHistory is the per-process recall list. Entries are oldest-first and
// consecutive duplicates are collapsed; idx == len(entries) means "not
// navigating", with draft holding whatever was typed before recall started.
type promptHistory struct {
	entries []string
	idx     int
	draft   string
}

func (h *promptHistory) add(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == text {
		h.idx = len(h.entries)
		h.draft = ""
		return
	}
	h.entries = append(h.entries, text)
	if len(h.entries) > promptHistoryLimit {
		h.entries = h.entries[len(h.entries)-promptHistoryLimit:]
	}
	h.idx = len(h.entries)
	h.draft = ""
}

// prev returns the previous (older) entry, saving the current text as the draft
// when recall begins.
func (h *promptHistory) prev(current string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	if h.idx >= len(h.entries) {
		h.draft = current
		h.idx = len(h.entries)
	}
	if h.idx > 0 {
		h.idx--
	}
	return h.entries[h.idx], true
}

// next returns the following (newer) entry, or the saved draft at the end.
func (h *promptHistory) next() (string, bool) {
	if len(h.entries) == 0 || h.idx >= len(h.entries) {
		return "", false
	}
	h.idx++
	if h.idx >= len(h.entries) {
		return h.draft, true
	}
	return h.entries[h.idx], true
}

// resetNavigation puts recall back at the newest entry; submission does this so
// the next recall starts from the prompt as submitted (the list is kept).
func (h *promptHistory) resetNavigation() {
	h.idx = len(h.entries)
	h.draft = ""
}

// navigating reports whether recall is mid-list (used by esc to restore draft).
func (h *promptHistory) navigating() bool {
	return h.idx < len(h.entries)
}

// restoreDraft returns the pre-navigation draft and ends navigation.
func (h *promptHistory) restoreDraft() string {
	d := h.draft
	h.resetNavigation()
	return d
}

// --- persistence ----------------------------------------------------------
//
// Persistence is opt-in (enablePersistentHistory, wired by Run): the list is
// stored per workspace under the config dir as history/<sha8-of-workspace>.json.
// Keeping it opt-in means the hundreds of model tests that submit prompts never
// touch the real user config; persistence tests isolate APPDATA/XDG_CONFIG_HOME.

// promptHistoryPath is the per-workspace history file: first 8 hex chars of the
// workspace's SHA-256, so distinct roots never collide and the name is stable.
func promptHistoryPath(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	name := hex.EncodeToString(sum[:])[:8] + ".json"
	return filepath.Join(config.ConfigDir(), "history", name)
}

// enablePersistentHistory loads the workspace's saved list and turns on saving.
func (m *Model) enablePersistentHistory() {
	m.historyFile = promptHistoryPath(m.workspaceRoot)
	m.persistHistory = true
	if entries, err := loadPromptHistoryFile(m.historyFile); err == nil {
		m.promptHistory.entries = entries
		m.promptHistory.resetNavigation()
	}
}

func loadPromptHistoryFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if len(entries) > promptHistoryLimit {
		entries = entries[len(entries)-promptHistoryLimit:]
	}
	return entries, nil
}

// savePromptHistory writes the list after each send. The payload is bounded
// (≤200 short entries), so a synchronous owner-only write is simple and safe.
func (m *Model) savePromptHistory() {
	if !m.persistHistory || m.historyFile == "" {
		return
	}
	data, err := json.Marshal(m.promptHistory.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.historyFile), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(m.historyFile, data, 0o600)
}
