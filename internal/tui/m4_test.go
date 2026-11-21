package tui

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/tools"
)

// --- helpers --------------------------------------------------------------

func m4Model(w, h int) Model {
	m := newTestModel(nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

// m4ChatModel is a wide, non-compact model focused on the transcript with n
// two-line user messages, parked at the top with no highlight.
func m4ChatModel(n int) Model {
	m := m4Model(140, 40)
	for i := 0; i < n; i++ {
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: "line " + strings.Repeat("x", 10)})
	}
	m.setFocus(focusMain)
	m.focusedItem = -1
	m.viewport.GotoTop()
	m.flushNow()
	return m
}

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// --- dispatch order contract ----------------------

func TestDispatchCtrlCBeforeDialogs(t *testing.T) {
	t.Run("idle palette: ctrl+c opens quit, not swallowed", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		if !m.overlays.has(overlayPalette) {
			t.Fatal("palette did not open")
		}
		m = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !m.overlays.has(overlayQuit) {
			t.Fatal("ctrl+c with palette open did not open the quit dialog")
		}
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if !cmdQuits(t, cmd) {
			t.Fatal("second ctrl+c in quit dialog did not quit")
		}
	})

	t.Run("streaming palette: ctrl+c cancels the turn", func(t *testing.T) {
		cancelCh := make(chan struct{}, 1)
		m := newTestModel(cancelCh)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		m = next.(Model)
		m.streaming = true
		m = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		m = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		select {
		case <-cancelCh:
		default:
			t.Fatal("ctrl+c while streaming+palette did not cancel the turn")
		}
		if !m.overlays.has(overlayPalette) {
			t.Fatal("cancel displaced the palette")
		}
	})
}

func TestDispatchApprovalCtrlCIsDenyQuit(t *testing.T) {
	// Security invariant: approval keeps ctrl+c = deny + quit, even though
	// ctrl+c is otherwise handled before dialogs.
	m := p6Model(nil)
	reply := make(chan bool, 1)
	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{
		ToolName: "shell_exec", Args: json.RawMessage(`{}`), ReplyCh: reply,
	}})
	m = next.(Model)
	m.grace.skip()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !cmdQuits(t, cmd) {
		t.Fatal("approval ctrl+c did not quit")
	}
	select {
	case ok := <-reply:
		if ok {
			t.Fatal("approval ctrl+c approved instead of denying")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval ctrl+c never replied")
	}
}

func TestDispatchDialogsSeeKeysFirst(t *testing.T) {
	m := p6Model(nil)
	m = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	// 'h' would scroll/select in chat, but the palette owns it as a filter.
	m = pressKey(m, keyRune('h'))
	if got := m.palette.FilterValue(); got != "h" {
		t.Fatalf("dialog did not see the key first: filter=%q", got)
	}
}

func TestDispatchEscBusyCancels(t *testing.T) {
	cancelCh := make(chan struct{}, 1)
	m := newTestModel(cancelCh)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = next.(Model)
	m.streaming = true
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	select {
	case <-cancelCh:
	default:
		t.Fatal("esc while busy did not cancel")
	}
}

// --- focus model ----------------------------------------------------------

func TestFocusCycling(t *testing.T) {
	m := m4Model(140, 40)
	if m.focus != focusEditor {
		t.Fatalf("ready focus = %v, want editor", m.focus)
	}
	if !m.textarea.Focused() {
		t.Fatal("editor focus did not focus the textarea")
	}

	// Tab on a non-slash prompt cycles editor -> main.
	m = pressTab(m)
	if m.focus != focusMain {
		t.Fatalf("tab focus = %v, want main", m.focus)
	}
	if m.textarea.Focused() {
		t.Fatal("main focus left the textarea focused")
	}
	// Tab cycles back to editor.
	m = pressTab(m)
	if m.focus != focusEditor {
		t.Fatalf("tab back focus = %v, want editor", m.focus)
	}

	// l/right moves main -> sidebar only when non-compact.
	m.setFocus(focusMain)
	m = pressKey(m, keyRune('l'))
	if m.focus != focusSidebar {
		t.Fatalf("l focus = %v, want sidebar", m.focus)
	}
	// h/left moves sidebar -> main.
	m = pressKey(m, keyRune('h'))
	if m.focus != focusMain {
		t.Fatalf("h focus = %v, want main", m.focus)
	}
	// right arrow also moves to sidebar.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.focus != focusSidebar {
		t.Fatalf("right focus = %v, want sidebar", m.focus)
	}
	// tab from sidebar goes to editor.
	m = pressTab(m)
	if m.focus != focusEditor {
		t.Fatalf("sidebar tab focus = %v, want editor", m.focus)
	}
}

func TestFocusCompactGuard(t *testing.T) {
	m := m4Model(140, 40)
	m.setFocus(focusMain)
	// In compact mode l must NOT move to the (hidden) sidebar.
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.setFocus(focusMain)
	m = pressKey(m, keyRune('l'))
	if m.focus != focusMain {
		t.Fatalf("compact l moved focus to %v, want main", m.focus)
	}
	// Entering compact while sidebar-focused forces the editor.
	m = m4Model(140, 40)
	m.setFocus(focusSidebar)
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	if m.focus != focusEditor {
		t.Fatalf("entering compact from sidebar focus = %v, want editor", m.focus)
	}
}

func TestFocusNoneBeforeReady(t *testing.T) {
	m := newTestModel(nil)
	if m.focus != focusNone {
		t.Fatalf("pre-ready focus = %v, want none", m.focus)
	}
}

// --- chat keys ------------------------------------------------------------

func TestChatLineScroll(t *testing.T) {
	m := m4ChatModel(30)
	top := m.viewport.YOffset()
	m = pressKey(m, keyRune('j'))
	if got := m.viewport.YOffset(); got != top+1 {
		t.Fatalf("j offset = %d, want %d", got, top+1)
	}
	m = pressKey(m, keyRune('k'))
	if got := m.viewport.YOffset(); got != top {
		t.Fatalf("k offset = %d, want %d", got, top)
	}
	// ctrl+j / ctrl+k are aliases.
	m = pressKey(m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := m.viewport.YOffset(); got != top+1 {
		t.Fatalf("ctrl+j offset = %d, want %d", got, top+1)
	}
}

func TestChatItemSelect(t *testing.T) {
	m := m4ChatModel(30)
	m.focusedItem = 0
	m = pressKey(m, keyRune('J'))
	if m.focusedItem != 1 {
		t.Fatalf("J selected %d, want 1", m.focusedItem)
	}
	m = pressKey(m, keyRune('K'))
	if m.focusedItem != 0 {
		t.Fatalf("K selected %d, want 0", m.focusedItem)
	}
	// shift+down / shift+up aliases.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if m.focusedItem != 1 {
		t.Fatalf("shift+down selected %d, want 1", m.focusedItem)
	}
	// Selecting far down scrolls the item into view.
	m.focusedItem = 0
	m.viewport.GotoTop()
	for i := 0; i < 25; i++ {
		m = pressKey(m, keyRune('J'))
	}
	if m.focusedItem != 25 {
		t.Fatalf("selected %d, want 25", m.focusedItem)
	}
	s := m.spans[25]
	if s.start < m.viewport.YOffset() {
		t.Fatalf("selected item not scrolled into view: start=%d top=%d", s.start, m.viewport.YOffset())
	}
}

func TestChatHalfPageSelectsEdges(t *testing.T) {
	m := m4ChatModel(40)
	m.viewport.GotoTop()
	m = pressKey(m, keyRune('d'))
	if m.focusedItem != m.lastVisibleItem() {
		t.Fatalf("d selected %d, want last visible %d", m.focusedItem, m.lastVisibleItem())
	}
	m = pressKey(m, keyRune('u'))
	if m.focusedItem != m.firstVisibleItem() {
		t.Fatalf("u selected %d, want first visible %d", m.focusedItem, m.firstVisibleItem())
	}
}

func TestChatTopBottomAndFollow(t *testing.T) {
	m := m4ChatModel(40)
	m = pressKey(m, keyRune('G'))
	if !m.viewport.AtBottom() || m.focusedItem != len(m.messages)-1 {
		t.Fatalf("G did not go to bottom/select last: bottom=%v sel=%d", m.viewport.AtBottom(), m.focusedItem)
	}
	m = pressKey(m, keyRune('g'))
	if !m.viewport.AtTop() || m.focusedItem != 0 {
		t.Fatalf("g did not go to top/select first: top=%v sel=%d", m.viewport.AtTop(), m.focusedItem)
	}
	// home/end aliases.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if !m.viewport.AtBottom() {
		t.Fatal("end did not go to bottom")
	}
	// ctrl+end is global: works from editor focus too.
	m.setFocus(focusEditor)
	m.viewport.GotoTop()
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if !m.viewport.AtBottom() || m.focusedItem != len(m.messages)-1 {
		t.Fatal("ctrl+end did not re-arm follow from editor focus")
	}
}

func TestChatPageKeys(t *testing.T) {
	m := m4ChatModel(60)
	m.viewport.GotoTop()
	m = pressKey(m, keyRune('f'))
	if m.viewport.YOffset() == 0 {
		t.Fatal("f did not page down")
	}
	off := m.viewport.YOffset()
	m = pressKey(m, keyRune('b'))
	if m.viewport.YOffset() >= off {
		t.Fatal("b did not page up")
	}
	// pgdown/pgup alias the page keys in chat focus.
	m.viewport.GotoTop()
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.viewport.YOffset() == 0 {
		t.Fatal("pgdown did not page in chat focus")
	}
}

func TestSpaceExpandsNotPages(t *testing.T) {
	m := m4Model(140, 40)
	long := strings.Repeat("row\n", 24) // 25 lines incl. label
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: long})
	m.setFocus(focusMain)
	m.focusedItem = 0
	m.viewport.GotoTop()
	m.flushNow()

	collapsed := lipglossHeight(m.renderChatItemView(0, m.messages[0], true))
	if collapsed != collapseLines+1 {
		t.Fatalf("collapsed height = %d, want %d", collapsed, collapseLines+1)
	}
	if !strings.Contains(stripANSI(m.renderMessages()), "space to expand") {
		t.Fatal("collapsed item missing the expand affordance")
	}
	off := m.viewport.YOffset()
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if !m.expanded[0] {
		t.Fatal("space did not expand the selected item")
	}
	expanded := lipglossHeight(m.renderChatItemView(0, m.messages[0], true))
	if expanded <= collapsed {
		t.Fatalf("expanded height %d did not grow beyond collapsed %d", expanded, collapsed)
	}
	if m.viewport.YOffset() != off {
		t.Fatalf("space paged the viewport (offset %d -> %d); it must only expand", off, m.viewport.YOffset())
	}
	// space again collapses.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	if m.expanded[0] {
		t.Fatal("second space did not collapse")
	}
}

func lipglossHeight(s string) int { return strings.Count(s, "\n") + 1 }

func TestChatEscClearsHighlight(t *testing.T) {
	m := m4ChatModel(5)
	m.focusedItem = 2
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.focusedItem != -1 {
		t.Fatalf("esc left highlight at %d, want -1", m.focusedItem)
	}
}

func TestChatCopyAllKeysAndOSC52(t *testing.T) {
	// Pure builder: base64 payload round-trips, correct OSC 52 framing.
	content := "hello clipboard"
	seq := osc52Sequence(content)
	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\x1b\\") {
		t.Fatalf("osc52 framing wrong: %q", seq)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x1b\\")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || string(decoded) != content {
		t.Fatalf("osc52 payload = %q err=%v, want %q", decoded, err, content)
	}

	for _, k := range []rune{'c', 'y', 'C', 'Y'} {
		m := m4ChatModel(3)
		m.focusedItem = 1
		m.messages[1].Content = "copy me"
		cmd := m.chatCopy()
		if cmd == nil {
			t.Fatalf("%c copy returned no command", k)
		}
		if !m.toast.set || m.toast.kind != toastSuccess {
			t.Fatalf("%c copy did not raise a success toast", k)
		}
		// The key dispatch routes all four to the same central copy.
		m2 := m4ChatModel(3)
		m2.focusedItem = 1
		m2 = pressKey(m2, keyRune(k))
		if !m2.toast.set {
			t.Fatalf("%c key did not copy", k)
		}
	}
}

func TestChatHorizScrollClamp(t *testing.T) {
	if got := horizClamp(0, -5, 100); got != 0 {
		t.Fatalf("horizClamp below zero = %d", got)
	}
	if got := horizClamp(98, 5, 100); got != 100 {
		t.Fatalf("horizClamp above max = %d, want 100", got)
	}
	if got := horizClamp(10, 5, 100); got != 15 {
		t.Fatalf("horizClamp step = %d, want 15", got)
	}

	m := m4Model(140, 40)
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: strings.Repeat("z", 300)})
	m.setFocus(focusMain)
	m.focusedItem = 0
	maxOff := m.itemMaxHorizOffset(m.messages[0])
	if maxOff <= 0 {
		t.Fatalf("long line max offset = %d, want > 0", maxOff)
	}
	m = pressKey(m, keyRune('L'))
	if m.hOffset[0] != horizStep {
		t.Fatalf("L offset = %d, want %d", m.hOffset[0], horizStep)
	}
	m = pressKey(m, keyRune('H'))
	if m.hOffset[0] != 0 {
		t.Fatalf("H offset = %d, want 0", m.hOffset[0])
	}
	// Clamp at the longest line: many L presses never exceed maxOff.
	for i := 0; i < 100; i++ {
		m = pressKey(m, keyRune('L'))
	}
	if m.hOffset[0] != maxOff {
		t.Fatalf("L clamped to %d, want max %d", m.hOffset[0], maxOff)
	}
}

func TestSidebarScrollKeys(t *testing.T) {
	m := m4Model(140, 40)
	m.sidebar = SidebarInfo{SessionID: "s", MCPServers: []string{"a", "b", "c", "d", "e"}}
	m.setFocus(focusSidebar)
	maxOff := m.sidebarMaxOffset()
	// j/k route to four-line steps, clamped to the scrollable range (the
	// sidebar only scrolls when its content overflows, ).
	m = pressKey(m, keyRune('j'))
	if m.sidebarOffset != min(sidebarScrollStep, maxOff) {
		t.Fatalf("sidebar j offset = %d, want %d", m.sidebarOffset, min(sidebarScrollStep, maxOff))
	}
	m = pressKey(m, keyRune('G'))
	if m.sidebarOffset != maxOff {
		t.Fatalf("sidebar G offset = %d, want max %d", m.sidebarOffset, maxOff)
	}
	m = pressKey(m, keyRune('g'))
	if m.sidebarOffset != 0 {
		t.Fatalf("sidebar g offset = %d, want 0", m.sidebarOffset)
	}
	// Clamp math: never below zero, never beyond max.
	m.sidebarOffset = 0
	m.scrollSidebar(-sidebarScrollStep)
	if m.sidebarOffset != 0 {
		t.Fatalf("scroll below zero = %d", m.sidebarOffset)
	}
	m.sidebarOffset = maxOff
	m.scrollSidebar(sidebarScrollStep)
	if m.sidebarOffset != maxOff {
		t.Fatalf("scroll beyond max = %d, want %d", m.sidebarOffset, maxOff)
	}
}

// --- palette alias --------------------------------------------------------

func TestPaletteCtrlPAndCtrlKAlias(t *testing.T) {
	m := p6Model(nil)
	m = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !m.overlays.has(overlayPalette) {
		t.Fatal("ctrl+p did not open the palette")
	}
	// ctrl+p toggles closed.
	m = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.overlays.has(overlayPalette) {
		t.Fatal("ctrl+p did not close the palette")
	}
	// ctrl+k still opens (alias).
	m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if !m.overlays.has(overlayPalette) {
		t.Fatal("ctrl+k alias did not open the palette")
	}
	// ctrl+n navigates down; up navigates up (ctrl+p toggles the palette closed).
	m = pressKey(m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
}

// --- @file completion popup ----------------------------------------------

func TestFileCompletionTrigger(t *testing.T) {
	newM := func() Model {
		m := p6Model(nil)
		m.workspaceRoot = t.TempDir()
		return m
	}
	t.Run("empty prompt", func(t *testing.T) {
		m := newM()
		m = pressKey(m, keyRune('@'))
		if !m.fileComp.open {
			t.Fatal("@ at prompt start did not open the popup")
		}
	})
	t.Run("after whitespace", func(t *testing.T) {
		m := newM()
		m.textarea.SetValue("look at ")
		m.textarea.CursorEnd()
		m = pressKey(m, keyRune('@'))
		if !m.fileComp.open {
			t.Fatal("@ after whitespace did not open the popup")
		}
	})
	t.Run("mid word does not trigger", func(t *testing.T) {
		m := newM()
		m.textarea.SetValue("email")
		m.textarea.CursorEnd()
		m = pressKey(m, keyRune('@'))
		if m.fileComp.open {
			t.Fatal("@ mid-word opened the popup")
		}
	})
}

func TestFileCompletionRankTiers(t *testing.T) {
	items := []string{
		"src/main.go",
		"main_test.go",
		"docs/main.md",
		"src/util.go",
		"main.go",
	}
	got := rankFiles(items, "main")
	// Tier 0 (exact stem "main"): main.go, src/main.go and docs/main.md all
	// qualify and keep their stable input order. Tier 1 (filename prefix):
	// main_test.go. src/util.go matches nothing and drops out.
	for _, p := range got {
		if p == "src/util.go" {
			t.Fatalf("non-matching item survived: %v", got)
		}
	}
	idxPrefix := indexOf(got, "main_test.go")
	if idxPrefix < 0 {
		t.Fatalf("prefix match dropped: %v", got)
	}
	for _, stem := range []string{"main.go", "src/main.go", "docs/main.md"} {
		i := indexOf(got, stem)
		if i < 0 || i > idxPrefix {
			t.Fatalf("tier order wrong: %q at %d, prefix at %d (%v)", stem, i, idxPrefix, got)
		}
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestFileCompletionGeometry(t *testing.T) {
	w, h := fileCompGeometry([]string{"ab"})
	if w != fileCompMinWidth || h != 1 {
		t.Fatalf("small geometry = %dx%d, want %dx1", w, h, fileCompMinWidth)
	}
	long := strings.Repeat("x", 200)
	w, _ = fileCompGeometry([]string{long})
	if w != fileCompMaxWidth {
		t.Fatalf("wide geometry = %d, want clamp %d", w, fileCompMaxWidth)
	}
	many := make([]string, 30)
	for i := range many {
		many[i] = "f"
	}
	_, h = fileCompGeometry(many)
	if h != fileCompMaxHeight {
		t.Fatalf("tall geometry = %d, want clamp %d", h, fileCompMaxHeight)
	}
	// width = widest + 2, clamped to [10,100]: a 6-cell item floors at 10,
	// a 20-cell item yields 22.
	w, _ = fileCompGeometry([]string{"abcdef"})
	if w != fileCompMinWidth {
		t.Fatalf("short width = %d, want min clamp %d", w, fileCompMinWidth)
	}
	w, _ = fileCompGeometry([]string{strings.Repeat("x", 20)})
	if w != 22 {
		t.Fatalf("width = %d, want widest+2 = 22", w)
	}
}

func TestFileCompletionRenderReversed(t *testing.T) {
	m := p6Model(nil)
	m.fileComp = fileCompletion{open: true, filtered: []string{"alpha", "beta", "gamma"}, sel: 0}
	out := stripANSI(m.renderFileCompletion())
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Reversed: first filtered item on the bottom row, last on top.
	if !strings.Contains(rows[2], "alpha") {
		t.Fatalf("bottom row = %q, want alpha", rows[2])
	}
	if !strings.Contains(rows[0], "gamma") {
		t.Fatalf("top row = %q, want gamma", rows[0])
	}
}

func TestFileCompletionKeysAndInsertion(t *testing.T) {
	open := func() Model {
		m := p6Model(nil)
		m.workspaceRoot = t.TempDir()
		m = pressKey(m, keyRune('@'))
		next, _ := m.Update(fileListMsg{files: []string{"alpha.go", "beta.go", "with space.go"}})
		return next.(Model)
	}

	t.Run("down/up move circularly", func(t *testing.T) {
		m := open()
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
		if m.fileComp.sel != 1 {
			t.Fatalf("down sel = %d", m.fileComp.sel)
		}
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		if m.fileComp.sel != 2 {
			t.Fatalf("up wrap sel = %d, want 2", m.fileComp.sel)
		}
	})

	t.Run("enter accepts and closes", func(t *testing.T) {
		m := open()
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if m.fileComp.open {
			t.Fatal("enter left the popup open")
		}
		if got := m.textarea.Value(); got != "alpha.go" {
			t.Fatalf("inserted %q, want alpha.go", got)
		}
	})

	t.Run("tab and ctrl+y accept", func(t *testing.T) {
		for _, k := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: 'y', Mod: tea.ModCtrl}} {
			m := open()
			m = pressKey(m, k)
			if m.fileComp.open || m.textarea.Value() != "alpha.go" {
				t.Fatalf("accept key %v failed: open=%v value=%q", k, m.fileComp.open, m.textarea.Value())
			}
		}
	})

	t.Run("ctrl+n inserts and keeps open", func(t *testing.T) {
		m := open()
		m = pressKey(m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
		if !m.fileComp.open {
			t.Fatal("ctrl+n closed the popup")
		}
		if !strings.Contains(m.textarea.Value(), ".go") {
			t.Fatalf("ctrl+n did not insert: %q", m.textarea.Value())
		}
	})

	t.Run("esc closes without inserting", func(t *testing.T) {
		m := open()
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.fileComp.open || m.textarea.Value() != "@" {
			t.Fatalf("esc failed: open=%v value=%q", m.fileComp.open, m.textarea.Value())
		}
	})

	t.Run("space closes and types a space", func(t *testing.T) {
		m := open()
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		if m.fileComp.open {
			t.Fatal("space left the popup open")
		}
		if got := m.textarea.Value(); got != "@ " {
			t.Fatalf("space value = %q, want %q", got, "@ ")
		}
	})

	t.Run("quoting on insertion", func(t *testing.T) {
		m := open()
		m.fileComp.sel = indexOf(m.fileComp.filtered, "with space.go")
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if got := m.textarea.Value(); got != "\"with space.go\"" {
			t.Fatalf("quoted insertion = %q", got)
		}
	})

	t.Run("typing re-filters", func(t *testing.T) {
		m := open()
		m = pressKey(m, keyRune('b'))
		if m.fileComp.query != "b" {
			t.Fatalf("query = %q, want b", m.fileComp.query)
		}
		for _, p := range m.fileComp.filtered {
			if !strings.Contains(strings.ToLower(p), "b") {
				t.Fatalf("re-filter kept non-match %q", p)
			}
		}
	})

	t.Run("backspace past @ closes", func(t *testing.T) {
		m := open()
		m = pressKey(m, keyRune('a')) // "@a"
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
		if m.fileComp.open {
			t.Fatal("deleting the @ left the popup open")
		}
	})
}

func TestListWorkspaceFilesBounded(t *testing.T) {
	root := t.TempDir()
	// Skipped directories must not contribute files.
	for _, d := range []string{".git", "node_modules", "vendor"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
		_ = os.WriteFile(filepath.Join(root, d, "hidden.txt"), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(root, "keep.go"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sub", "nested.go"), []byte("x"), 0o644)

	got := listWorkspaceFiles(root)
	if indexOf(got, "keep.go") < 0 || indexOf(got, "sub/nested.go") < 0 {
		t.Fatalf("listing missing real files: %v", got)
	}
	for _, p := range got {
		if strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, "vendor/") {
			t.Fatalf("listing included a skipped dir: %v", got)
		}
	}
}

// --- persistent prompt history -------------------------------------------

func TestPromptHistoryPersistenceRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	m1 := p6Model(nil)
	m1.workspaceRoot = "/ws/alpha"
	m1.enablePersistentHistory()
	m1.textarea.SetValue("first prompt")
	m1 = pressEnter(m1)
	m1 = drive(m1, agentDoneMsg{})
	m1.textarea.SetValue("second prompt")
	m1 = pressEnter(m1)

	// A fresh model for the same workspace loads the saved list.
	m2 := p6Model(nil)
	m2.workspaceRoot = "/ws/alpha"
	m2.enablePersistentHistory()
	if len(m2.promptHistory.entries) != 2 ||
		m2.promptHistory.entries[0] != "first prompt" ||
		m2.promptHistory.entries[1] != "second prompt" {
		t.Fatalf("round-trip entries = %v", m2.promptHistory.entries)
	}

	// A different workspace is isolated.
	m3 := p6Model(nil)
	m3.workspaceRoot = "/ws/beta"
	m3.enablePersistentHistory()
	if len(m3.promptHistory.entries) != 0 {
		t.Fatalf("workspace isolation leaked: %v", m3.promptHistory.entries)
	}

	// The file lives under config/history with an 8-hex-char name.
	path := promptHistoryPath("/ws/alpha")
	if !strings.Contains(filepath.ToSlash(path), "/history/") {
		t.Fatalf("history path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("history file not written: %v", err)
	}
}

func TestPromptHistoryBoundAndDedup(t *testing.T) {
	var h promptHistory
	for i := 0; i < promptHistoryLimit+50; i++ {
		h.add(strings.Repeat("p", i%7+1) + string(rune('a'+i%26)))
	}
	if len(h.entries) != promptHistoryLimit {
		t.Fatalf("bound = %d, want %d", len(h.entries), promptHistoryLimit)
	}
	// Consecutive duplicates collapse.
	var d promptHistory
	d.add("same")
	d.add("same")
	if len(d.entries) != 1 {
		t.Fatalf("dedup = %v", d.entries)
	}
}

func TestPromptHistoryEscRestoresDraft(t *testing.T) {
	m := p6Model(nil)
	m.promptHistory.add("one")
	m.promptHistory.add("two")
	m.textarea.SetValue("my draft")
	m.textarea.CursorEnd()
	m = pressAlt(m, 'p') // -> two, draft saved
	m = pressAlt(m, 'p') // -> one
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := m.textarea.Value(); got != "my draft" {
		t.Fatalf("esc restored %q, want the pre-navigation draft", got)
	}
	if m.promptHistory.navigating() {
		t.Fatal("esc did not end navigation")
	}
}

func TestPromptHistoryDownAtEndRecalls(t *testing.T) {
	m := p6Model(nil)
	m.promptHistory.add("one")
	m.promptHistory.add("two")
	m = pressAlt(m, 'p') // two
	m = pressAlt(m, 'p') // one
	// Down at end-of-text walks newer.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.textarea.Value(); got != "two" {
		t.Fatalf("down value = %q, want two", got)
	}
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("down past newest = %q, want empty draft", got)
	}
}

// --- status toasts --------------------------------------------------------

func TestToastTTLAndSequenceStamp(t *testing.T) {
	orig := msgNow
	defer func() { msgNow = orig }()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgNow = func() time.Time { return start }

	m := p6Model(nil)
	m.showToast(toastInfo, "first")
	seq1 := m.toast.seq
	if !m.toastActive(start.Add(4 * time.Second)) {
		t.Fatal("toast expired before its 5s TTL")
	}
	if m.toastActive(start.Add(5 * time.Second)) {
		t.Fatal("toast still active at its TTL boundary")
	}

	// A newer toast gets a newer stamp; the stale expiry cannot clear it.
	m.showToast(toastSuccess, "second")
	seq2 := m.toast.seq
	if seq2 == seq1 {
		t.Fatal("toast sequence did not advance")
	}
	m.expireToast(seq1)
	if !m.toast.set || m.toast.body != "second" {
		t.Fatal("stale expiry cleared the newer toast")
	}
	m.expireToast(seq2)
	if m.toast.set {
		t.Fatal("matching expiry did not clear the toast")
	}
}

func TestToastAnatomy(t *testing.T) {
	m := p6Model(nil)
	m.showToast(toastSuccess, "copied")
	bar := m.renderToast(60)
	plain := stripANSI(bar)
	if w := cellWidth(plain); w != 60 {
		t.Fatalf("toast width = %d, want 60", w)
	}
	if !strings.Contains(plain, "OKAY!") || !strings.Contains(plain, "copied") {
		t.Fatalf("toast missing indicator/body: %q", plain)
	}
	if !strings.HasSuffix(plain, " ") {
		t.Fatalf("toast not right-padded to a solid bar: %q", plain)
	}

	// Newlines flatten; long bodies truncate with an ellipsis.
	m.showToast(toastError, "line one\nline two")
	if strings.Contains(stripANSI(m.renderToast(60)), "\n") {
		t.Fatal("toast body kept a newline")
	}
	if !strings.Contains(stripANSI(m.renderToast(60)), "line one line two") {
		t.Fatal("toast body did not flatten newlines to spaces")
	}
	m.showToast(toastWarning, strings.Repeat("x", 200))
	if !strings.Contains(stripANSI(m.renderToast(40)), "…") {
		t.Fatal("long toast body did not truncate with …")
	}

	// Each kind uses its indicator.
	for kind, label := range map[toastKind]string{
		toastSuccess: "OKAY!", toastInfo: "HEY!", toastWarning: "WARNING", toastError: "ERROR",
	} {
		m.showToast(kind, "x")
		if !strings.Contains(stripANSI(m.renderToast(60)), label) {
			t.Fatalf("kind %d missing indicator %q", kind, label)
		}
	}
}

func TestToastReplacesStatusRow(t *testing.T) {
	m := p6Model(nil)
	m.showToast(toastSuccess, "saved")
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "OKAY!") {
		t.Fatal("active toast did not replace the status row")
	}
}

func TestErrorToolResultToasts(t *testing.T) {
	m := p6Model(nil)
	next, _ := m.Update(agentToolMsg{name: "file_write", result: "permission denied", isError: true})
	m = next.(Model)
	if !m.toast.set || m.toast.kind != toastError {
		t.Fatal("error tool result did not raise an ERROR toast")
	}
	if !strings.Contains(m.toast.body, "permission denied") {
		t.Fatalf("error toast body = %q", m.toast.body)
	}
}

func TestSessionGrantToasts(t *testing.T) {
	m := p6Model(nil)
	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{
		ToolName: "shell_exec", Args: json.RawMessage(`{}`), ReplyCh: make(chan bool, 1),
	}})
	m = next.(Model)
	m.grace.skip()
	next, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(Model)
	if !m.toast.set || m.toast.kind != toastSuccess {
		t.Fatal("session grant did not raise a success toast")
	}
}

// --- keymap contract tests -----------------------------------------------

func TestM4KeymapParity(t *testing.T) {
	want := []struct {
		key    string
		ctx    keyContext
		action keyAction
	}{
		// Palette alias + global follow.
		{"ctrl+p", keyCtxGlobal, actPaletteOpen},
		{"ctrl+k", keyCtxGlobal, actPaletteOpen},
		{"ctrl+end", keyCtxGlobal, actFollowBottom},
		// Chat.
		{"j", keyCtxChat, actChatLineDown},
		{"k", keyCtxChat, actChatLineUp},
		{"ctrl+j", keyCtxChat, actChatLineDown},
		{"ctrl+k", keyCtxChat, actChatLineUp},
		{"J", keyCtxChat, actChatItemNext},
		{"K", keyCtxChat, actChatItemPrev},
		{"shift+down", keyCtxChat, actChatItemNext},
		{"shift+up", keyCtxChat, actChatItemPrev},
		{"u", keyCtxChat, actChatHalfUp},
		{"d", keyCtxChat, actChatHalfDown},
		{"b", keyCtxChat, actChatPageUp},
		{"f", keyCtxChat, actChatPageDown},
		{"pgup", keyCtxChat, actChatPageUp},
		{"pgdown", keyCtxChat, actChatPageDown},
		{"g", keyCtxChat, actChatTop},
		{"home", keyCtxChat, actChatTop},
		{"G", keyCtxChat, actChatBottom},
		{"end", keyCtxChat, actChatBottom},
		{"space", keyCtxChat, actChatExpand},
		{"c", keyCtxChat, actChatCopy},
		{"y", keyCtxChat, actChatCopy},
		{"C", keyCtxChat, actChatCopy},
		{"Y", keyCtxChat, actChatCopy},
		{"esc", keyCtxChat, actChatClearSel},
		{"H", keyCtxChat, actChatHorizLeft},
		{"L", keyCtxChat, actChatHorizRight},
		{"shift+left", keyCtxChat, actChatHorizLeft},
		{"shift+right", keyCtxChat, actChatHorizRight},
		{"l", keyCtxChat, actFocusSidebar},
		{"right", keyCtxChat, actFocusSidebar},
		{"tab", keyCtxChat, actFocusEditor},
		// Sidebar.
		{"j", keyCtxSidebar, actSidebarDown},
		{"k", keyCtxSidebar, actSidebarUp},
		{"g", keyCtxSidebar, actSidebarTop},
		{"G", keyCtxSidebar, actSidebarBot},
		{"h", keyCtxSidebar, actFocusMain},
		{"left", keyCtxSidebar, actFocusMain},
		{"tab", keyCtxSidebar, actFocusEditor},
		// Editor history down.
		{"down", keyCtxIdle, actRecallNext},
	}
	for _, w := range want {
		b, ok := lookupBinding(w.ctx, w.key)
		if !ok {
			t.Errorf("missing binding %q ctx %d", w.key, w.ctx)
			continue
		}
		if b.Action != w.action {
			t.Errorf("binding %q ctx %d = %q, want %q", w.key, w.ctx, b.Action, w.action)
		}
	}

	// space must NOT be bound to paging in chat (the shadowing fix).
	if b, _ := lookupBinding(keyCtxChat, "space"); b.Action == actChatPageDown {
		t.Fatal("space is bound to page down; it must only expand")
	}
	// Approval still owns ctrl+c as deny+quit (security invariant).
	if b, _ := lookupBinding(keyCtxApproval, "ctrl+c"); b.Action != actApprovalDenyQuit {
		t.Fatal("approval ctrl+c is not deny+quit")
	}
	_ = tools.ApprovalDeny
}
