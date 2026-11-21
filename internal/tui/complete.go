package tui

import (
	"sort"
	"strings"

	"github.com/mevarx/GoCode/internal/agent"
)

// completionState remembers an in-progress Tab cycle: the text prefix before
// the token, the candidates shown for it, and the value the cycle last wrote.
// Keeping the value lets repeated Tab presses advance the cycle instead of
// re-parsing the just-completed line as a fresh query.
type completionState struct {
	active     bool
	prefix     string
	value      string
	candidates []string
	index      int
}

// handleTabCompletion completes the token under the cursor. Command names
// complete after a leading slash; /model and /provider complete their first
// argument from the live model/picker data. Ambiguous prefixes extend to the
// longest common prefix and the candidates are echoed in the help line.
func (m *Model) handleTabCompletion() {
	if m.streaming || !m.textarea.Focused() {
		return
	}
	value := m.textarea.Value()
	if !strings.HasPrefix(value, "/") {
		m.clearCompletion()
		return
	}

	// Already cycling: advance without re-parsing the completed line.
	if m.completion.active && value == m.completion.value {
		m.cycleCompletion()
		return
	}

	sep := strings.IndexAny(value, " \t")
	if sep < 0 {
		m.applyCompletion(value, 0, value, prefixMatches(commandNames(), value))
		return
	}

	cmd := strings.ToLower(value[:sep])
	if cmd != "/model" && cmd != "/provider" {
		m.clearCompletion()
		return
	}

	rest := value[sep:]
	trimmed := strings.TrimLeft(rest, " \t")
	tokenStart := len(value) - len(trimmed)
	token := trimmed
	if strings.ContainsAny(token, " \t") {
		// A second argument is free text; nothing to complete.
		m.clearCompletion()
		return
	}

	m.applyCompletion(value, tokenStart, token, prefixMatches(m.completionPool(cmd), token))
}

// applyCompletion writes the completion for token, or opens a candidate cycle
// when the prefix matches more than one entry.
func (m *Model) applyCompletion(value string, tokenStart int, token string, candidates []string) {
	if len(candidates) == 0 {
		m.clearCompletion()
		return
	}
	base := value[:tokenStart]
	if len(candidates) == 1 {
		m.clearCompletion()
		m.setText(base + candidates[0] + " ")
		return
	}

	if common := commonPrefix(candidates); len(common) > len(token) {
		m.setText(base + common)
	} else {
		m.setText(value)
	}
	m.completion = completionState{
		active:     true,
		prefix:     base,
		value:      m.textarea.Value(),
		candidates: candidates,
		index:      0,
	}
	m.completionHint = candidateHint(candidates)
}

// cycleCompletion replaces the completed token with the next candidate.
func (m *Model) cycleCompletion() {
	if len(m.completion.candidates) == 0 {
		m.clearCompletion()
		return
	}
	idx := m.completion.index % len(m.completion.candidates)
	cand := m.completion.candidates[idx]
	m.completion.index = idx + 1
	m.setText(m.completion.prefix + cand + " ")
	m.completion.value = m.textarea.Value()
	m.completionHint = candidateHint(m.completion.candidates)
}

func (m *Model) clearCompletion() {
	m.completion = completionState{}
	m.completionHint = ""
}

// setText replaces the prompt content and leaves the cursor at the end.
func (m *Model) setText(text string) {
	m.textarea.SetValue(text)
	m.textarea.CursorEnd()
}

// completionPool returns the argument candidates for a command. /provider uses
// the live registry; /model prefers the model snapshot the picker already
// holds for the active provider, so completion never issues a network call.
func (m *Model) completionPool(cmd string) []string {
	if cmd == "/provider" {
		if m.registry == nil {
			return nil
		}
		names := m.registry.List()
		sort.Strings(names)
		return names
	}

	active := m.providerName
	if m.registry != nil {
		active = m.registry.ActiveName()
	}
	seen := make(map[string]bool)
	var out []string
	for _, item := range m.modelPicker.Items() {
		mi, ok := item.(ModelItem)
		if !ok || mi.Provider != active || seen[mi.Model] {
			continue
		}
		seen[mi.Model] = true
		out = append(out, mi.Model)
	}
	sort.Strings(out)
	return out
}

// commandNames lists the registry names in display order.
func commandNames() []string {
	specs := agent.Commands()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

// prefixMatches keeps the candidates whose name starts with token,
// case-insensitively, preserving pool order.
func prefixMatches(pool []string, token string) []string {
	lower := strings.ToLower(token)
	var out []string
	for _, name := range pool {
		if strings.HasPrefix(strings.ToLower(name), lower) {
			out = append(out, name)
		}
	}
	return out
}

// commonPrefix is the longest shared prefix of the candidates; Tab extends the
// typed token to it before listing the choices. Rune-wise, so a model name
// with non-ASCII characters can never be cut mid-character.
func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := []rune(values[0])
	for _, v := range values[1:] {
		r := []rune(v)
		n := len(prefix)
		if len(r) < n {
			n = len(r)
		}
		i := 0
		for i < n && prefix[i] == r[i] {
			i++
		}
		prefix = prefix[:i]
	}
	return string(prefix)
}

// candidateHint is the help-line echo of the choices, bounded so a long model
// list cannot crowd out the prompt.
const maxCompletionHint = 8

func candidateHint(candidates []string) string {
	shown := candidates
	suffix := ""
	if len(shown) > maxCompletionHint {
		shown = shown[:maxCompletionHint]
		suffix = " …"
	}
	return "Tab: " + strings.Join(shown, "  ") + suffix
}
