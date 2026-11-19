package tui

import "strings"

// promptHistoryLimit bounds the in-memory list; a session that submits
// thousands of prompts should not grow without bound.
const promptHistoryLimit = 100

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
// the next recall starts from the prompt as submitted.
func (h *promptHistory) resetNavigation() {
	h.idx = len(h.entries)
	h.draft = ""
}
