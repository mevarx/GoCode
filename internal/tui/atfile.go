package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/ignore"
)

// @file completion popup.
// Files are listed off the update loop, bounded in count and depth, ranked into
// four stable tiers, and rendered reversed so the popup grows upward above the
// prompt. MCP resource completion is omitted (GoCode has no resource registry).

const (
	fileCompMaxFiles  = 500
	fileCompMaxDepth  = 6
	fileCompMinWidth  = 10
	fileCompMaxWidth  = 100
	fileCompMaxHeight = 10
)

// fileCompletion is the popup state. atPos is the rune index of the triggering
// '@'; detached marks an insert-and-keep-open popup whose word has been replaced.
type fileCompletion struct {
	open     bool
	loading  bool
	detached bool
	atPos    int
	query    string
	items    []string
	filtered []string
	sel      int
}

// fileListMsg carries the asynchronous workspace listing into the popup.
type fileListMsg struct{ files []string }

// caretOffset is the cursor's rune offset into the prompt value.
func (m *Model) caretOffset() int {
	lines := strings.Split(m.textarea.Value(), "\n")
	off := 0
	row := m.textarea.Line()
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1
	}
	if row < len(lines) {
		off += m.textarea.Column()
	}
	return off
}

// maybeOpenFileCompletion opens the popup when a typed '@' sits at the prompt
// start or after whitespace (never mid-word). It returns the listing command.
func (m *Model) maybeOpenFileCompletion() tea.Cmd {
	if m.fileComp.open {
		return nil
	}
	if m.focus != focusEditor && m.focus != focusNone {
		return nil
	}
	value := []rune(m.textarea.Value())
	caret := m.caretOffset()
	atPos := caret - 1
	if atPos < 0 || atPos >= len(value) || value[atPos] != '@' {
		return nil
	}
	if atPos > 0 && !unicode.IsSpace(value[atPos-1]) {
		return nil
	}
	m.fileComp = fileCompletion{open: true, loading: true, atPos: atPos}
	m.reflow()
	return m.loadWorkspaceFiles()
}

func (m *Model) loadWorkspaceFiles() tea.Cmd {
	root := m.workspaceRoot
	return func() tea.Msg {
		return fileListMsg{files: listWorkspaceFiles(root)}
	}
}

// listWorkspaceFiles walks the workspace bounded by depth and count, skipping
// git/node_modules/vendor and anything the ignore matcher rejects. Paths are
// relative and slash-separated for display and insertion.
func listWorkspaceFiles(root string) []string {
	if root == "" {
		return nil
	}
	matcher := ignore.LoadMatcher(root)
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > fileCompMaxDepth || len(out) >= fileCompMaxFiles {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(out) >= fileCompMaxFiles {
				return
			}
			name := e.Name()
			full := filepath.Join(dir, name)
			rel := relSlash(root, full)
			if e.IsDir() {
				if name == ".git" || name == "node_modules" || name == "vendor" {
					continue
				}
				if matcher.IsIgnored(rel, true) {
					continue
				}
				walk(full, depth+1)
				continue
			}
			if matcher.IsIgnored(rel, false) {
				continue
			}
			out = append(out, rel)
		}
	}
	walk(root, 0)
	sort.Strings(out)
	return out
}

func relSlash(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		rel = full
	}
	return filepath.ToSlash(rel)
}

// wordEnd is the rune index where the @word ends (first whitespace or end).
func (fc *fileCompletion) wordEnd(value []rune) int {
	i := fc.atPos + 1
	for i < len(value) && !unicode.IsSpace(value[i]) {
		i++
	}
	return i
}

// syncFileCompletion re-derives the query after an edit and closes the popup
// when the caret moves before '@', the word no longer starts with '@', or the
// caret moves past the word (e.g. a space was typed).
func (m *Model) syncFileCompletion() {
	fc := &m.fileComp
	if !fc.open || fc.detached {
		return
	}
	value := []rune(m.textarea.Value())
	caret := m.caretOffset()
	if fc.atPos < 0 || fc.atPos >= len(value) || value[fc.atPos] != '@' || caret <= fc.atPos {
		m.closeFileCompletion()
		return
	}
	end := fc.wordEnd(value)
	if caret > end {
		m.closeFileCompletion()
		return
	}
	fc.query = string(value[fc.atPos+1 : end])
	m.refilterFiles()
}

func (m *Model) closeFileCompletion() {
	if !m.fileComp.open {
		return
	}
	m.fileComp = fileCompletion{}
	m.reflow()
}

func (m *Model) refilterFiles() {
	m.fileComp.filtered = rankFiles(m.fileComp.items, m.fileComp.query)
	if m.fileComp.sel >= len(m.fileComp.filtered) {
		m.fileComp.sel = 0
	}
}

// rankFiles filters and stable-sorts into four tiers: exact filename/stem,
// filename prefix, path substring, then subsequence; non-matches drop out.
func rankFiles(items []string, query string) []string {
	q := strings.ToLower(query)
	if q == "" {
		return append([]string(nil), items...)
	}
	type scored struct {
		path string
		tier int
	}
	var matched []scored
	for _, p := range items {
		if t := fileTier(p, q); t >= 0 {
			matched = append(matched, scored{p, t})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].tier < matched[j].tier })
	out := make([]string, len(matched))
	for i, s := range matched {
		out[i] = s.path
	}
	return out
}

func fileTier(path, q string) int {
	base := strings.ToLower(pathBase(path))
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case base == q || stem == q:
		return 0
	case strings.HasPrefix(base, q):
		return 1
	case strings.Contains(strings.ToLower(path), q):
		return 2
	case subsequence(strings.ToLower(path), q):
		return 3
	}
	return -1
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func subsequence(s, q string) bool {
	sr := []rune(s)
	qr := []rune(q)
	i := 0
	for _, r := range sr {
		if i < len(qr) && r == qr[i] {
			i++
		}
	}
	return i == len(qr)
}

// fileCompGeometry is width = widest visible item + 2 clamped [10,100], height =
// count clamped [1,10].
func fileCompGeometry(visible []string) (int, int) {
	h := len(visible)
	if h > fileCompMaxHeight {
		h = fileCompMaxHeight
	}
	if h < 1 {
		h = 1
	}
	widest := 0
	for _, item := range visible {
		if w := lipgloss.Width(item); w > widest {
			widest = w
		}
	}
	w := widest + 2
	if w < fileCompMinWidth {
		w = fileCompMinWidth
	}
	if w > fileCompMaxWidth {
		w = fileCompMaxWidth
	}
	return w, h
}

// fileWindow is the visible slice of the filtered list, keeping the selection
// in view; the popup renders it reversed so it grows upward from the prompt.
func (m *Model) fileWindow() (int, []string) {
	fc := m.fileComp
	n := len(fc.filtered)
	if n == 0 {
		return 0, nil
	}
	h := min(n, fileCompMaxHeight)
	start := fc.sel - h + 1
	if start < 0 {
		start = 0
	}
	if start > n-h {
		start = n - h
	}
	return start, fc.filtered[start : start+h]
}

// renderFileCompletion draws the popup rows reversed, selection highlighted.
func (m *Model) renderFileCompletion() string {
	fc := m.fileComp
	if !fc.open {
		return ""
	}
	tk := activeTheme.Tokens
	if len(fc.filtered) == 0 {
		label := "no matches"
		if fc.loading {
			label = "loading files…"
		}
		w, _ := fileCompGeometry([]string{label})
		row := " " + truncateToWidth(label, w-2)
		row += strings.Repeat(" ", max(0, w-lipgloss.Width(row)))
		return lipgloss.NewStyle().Background(tk.BgLessVisible).Foreground(tk.FgMoreSubtle).Render(row)
	}
	start, window := m.fileWindow()
	w, h := fileCompGeometry(window)
	rows := make([]string, h)
	for i := 0; i < h; i++ {
		idx := start + h - 1 - i // reversed: window[0] lands on the bottom row
		item := window[h-1-i]
		row := " " + truncateToWidth(item, w-2)
		row += strings.Repeat(" ", max(0, w-lipgloss.Width(row)))
		style := lipgloss.NewStyle().Background(tk.BgLessVisible).Foreground(tk.FgBase)
		if idx == fc.sel {
			style = style.Background(tk.Primary).Foreground(tk.OnPrimary)
		}
		rows[i] = style.Render(row)
	}
	return strings.Join(rows, "\n")
}

func (m *Model) moveFileSel(dir int) {
	n := len(m.fileComp.filtered)
	if n == 0 {
		return
	}
	m.fileComp.sel = (m.fileComp.sel + dir + n) % n
}

// updateFileCompletion owns every key while the popup is open.
func (m *Model) updateFileCompletion(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up":
		m.moveFileSel(-1)
		return nil
	case "down":
		m.moveFileSel(1)
		return nil
	case "enter", "tab", "ctrl+y":
		m.insertFileSelection()
		m.closeFileCompletion()
		return nil
	case "ctrl+n":
		m.moveFileSel(1)
		m.insertFileSelection()
		m.fileComp.detached = true
		return nil
	case "ctrl+p":
		m.moveFileSel(-1)
		m.insertFileSelection()
		m.fileComp.detached = true
		return nil
	case "esc", "alt+esc":
		m.closeFileCompletion()
		return nil
	case "space":
		// space closes the popup and types a normal space (does not insert).
		m.closeFileCompletion()
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return cmd
	default:
		// A detached popup closes on the next typed character; otherwise the
		// edit re-filters (or closes) via sync.
		if m.fileComp.detached {
			m.closeFileCompletion()
		}
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		m.syncFileCompletion()
		return cmd
	}
}

// insertFileSelection replaces the @word span with the selected path (quoted
// when it contains spaces), or inserts at the caret when detached.
func (m *Model) insertFileSelection() {
	fc := &m.fileComp
	if len(fc.filtered) == 0 {
		return
	}
	path := fc.filtered[fc.sel]
	if strings.ContainsAny(path, " \t") {
		path = "\"" + path + "\""
	}
	value := []rune(m.textarea.Value())
	if fc.atPos >= 0 && fc.atPos < len(value) && value[fc.atPos] == '@' {
		end := fc.wordEnd(value)
		newVal := string(value[:fc.atPos]) + path + string(value[end:])
		m.setText(newVal)
		m.setCaretOffset(fc.atPos + len([]rune(path)))
		return
	}
	m.textarea.InsertString(path)
}

// setCaretOffset places the cursor at a rune offset (single-line prompts are the
// common case; multi-line falls back to line/column navigation).
func (m *Model) setCaretOffset(off int) {
	lines := strings.Split(m.textarea.Value(), "\n")
	rem := off
	for i, ln := range lines {
		l := len([]rune(ln))
		if rem <= l {
			m.textarea.MoveToBegin()
			for j := 0; j < i; j++ {
				m.textarea.CursorDown()
			}
			m.textarea.SetCursorColumn(rem)
			return
		}
		rem -= l + 1
	}
	m.textarea.CursorEnd()
}
