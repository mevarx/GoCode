package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/session"
	"github.com/mevarx/GoCode/internal/tools"
)

// contracts: dialog frame geometry, badges, permission input grace and
// danger bias, quit confirmation, theme picker/editor round-trips, session
// switcher states and the sidebar data wiring.

// --- grace ---------------------------------------------------------------

func TestGraceQuietWindowAndCap(t *testing.T) {
	t0 := time.Now()
	var g approvalGrace
	g.open(t0)

	// A reflex key right after open is absorbed and restarts the quiet window.
	if !g.absorb(t0.Add(100 * time.Millisecond)) {
		t.Fatal("key at +100ms should be absorbed")
	}
	// 425ms of quiet after the last absorbed key ends the grace.
	if g.absorb(t0.Add(525 * time.Millisecond)) {
		t.Fatal("key at +525ms (425ms quiet) should be processed")
	}

	// Hard cap: continuous typing can never extend grace past 1500ms.
	g.open(t0)
	for _, at := range []int{300, 600, 900, 1200, 1400} {
		if !g.absorb(t0.Add(time.Duration(at) * time.Millisecond)) {
			t.Fatalf("key at +%dms should be absorbed under the cap", at)
		}
	}
	if g.absorb(t0.Add(1500 * time.Millisecond)) {
		t.Fatal("key at the 1500ms cap must be processed")
	}
}

func TestApprovalGraceAbsorbsReflexKeys(t *testing.T) {
	start := time.Now()
	orig := msgNow
	msgNow = func() time.Time { return start }
	defer func() { msgNow = orig }()

	m := newTestModel(nil)
	reply := make(chan bool, 1)
	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{
		ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`), ReplyCh: reply,
	}})
	m = next.(Model)

	// A reflexive y during grace is swallowed: no decision, no reply.
	next, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(Model)
	if !m.overlays.has(overlayApproval) {
		t.Fatal("grace absorbed the key but the modal closed anyway")
	}
	select {
	case <-reply:
		t.Fatal("grace must never produce a decision")
	default:
	}

	// After the quiet window the same key decides.
	msgNow = func() time.Time { return start.Add(500 * time.Millisecond) }
	next, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(Model)
	if m.overlays.has(overlayApproval) {
		t.Fatal("key after grace should resolve the approval")
	}
	select {
	case ok := <-reply:
		if !ok {
			t.Fatal("y after grace should approve")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reply never arrived")
	}
}

func TestApprovalGraceReopenSkip(t *testing.T) {
	start := time.Now()
	orig := msgNow
	msgNow = func() time.Time { return start }
	defer func() { msgNow = orig }()

	m := newTestModel(nil)
	req := ApprovalRequest{ToolName: "bash", Args: json.RawMessage(`{"command":"ls"}`), ReplyCh: make(chan bool, 1)}
	next, _ := m.Update(approvalRequestMsg{req: req})
	m = next.(Model)
	m.grace.skip()
	m, _ = m.resolveApproval(tools.ApprovalOnce, "✓ Approved", RoleSystem)

	// Same request reopened within 500ms skips grace entirely.
	msgNow = func() time.Time { return start.Add(100 * time.Millisecond) }
	req.ReplyCh = make(chan bool, 1)
	next, _ = m.Update(approvalRequestMsg{req: req})
	m = next.(Model)
	if m.grace.active {
		t.Fatal("reopen within 500ms should skip grace")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = next.(Model)
	if m.overlays.has(overlayApproval) {
		t.Fatal("key on a reopened request should act immediately")
	}
}

func TestApprovalDangerDefaultsToDeny(t *testing.T) {
	m := newTestModel(nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = updated.(Model)
	reply := make(chan bool, 1)
	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{
		ToolName: "bash", Args: json.RawMessage(`{}`), Danger: "deletes files", ReplyCh: reply,
	}})
	m = next.(Model)
	if m.approvalFocus != 1 {
		t.Fatalf("danger request focus = %d, want Deny (1)", m.approvalFocus)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "Potentially dangerous: deletes files") {
		t.Error("danger reason not rendered")
	}
	m.grace.skip()
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	select {
	case ok := <-reply:
		if ok {
			t.Fatal("reflexive Enter on a danger request must deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reply never arrived")
	}
}

func TestApprovalDiffToggles(t *testing.T) {
	m := newTestModel(nil)
	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{
		ToolName: "file_write", Args: json.RawMessage(`{}`), Preview: "@@ -1 +1 @@\n-a\n+b",
	}})
	m = next.(Model)
	m.grace.skip()
	next, _ = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = next.(Model)
	if !m.diffSplit {
		t.Fatal("t did not toggle split view")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	m = next.(Model)
	if !m.diffFullscreen {
		t.Fatal("f did not toggle fullscreen")
	}
}

// --- dialog chrome ---------------------------------------------------------

func TestDialogFrameGeometry(t *testing.T) {
	out := dialogFrame("Title", dialogNormal, "body", 40)
	if w := lipgloss.Width(out); w != 40 {
		t.Errorf("frame width = %d, want 40", w)
	}
	plain := stripANSI(out)
	for _, r := range []string{"╭", "╮", "╰", "╯", "╱", "Title", "body"} {
		if !strings.Contains(plain, r) {
			t.Errorf("frame missing %q", r)
		}
	}
	del := dialogFrame("Delete", dialogDeleting, "x", 40)
	if !strings.Contains(del, sgrFg(activeTheme.Tokens.Destructive)) {
		t.Error("deleting variant does not use the destructive frame colour")
	}
	ren := dialogFrame("Rename", dialogRenaming, "x", 40)
	if !strings.Contains(ren, sgrFg(activeTheme.Tokens.WarningSubtle)) {
		t.Error("renaming variant does not use the warning frame colour")
	}
}

func TestBadgeUsesContrastInk(t *testing.T) {
	fill := activeTheme.Tokens.Warning
	out := badge("HEY!", fill)
	if !strings.Contains(out, sgrBg(fill)) {
		t.Errorf("badge missing fill: %q", out)
	}
	if !strings.Contains(out, "\x1b[1;") {
		t.Errorf("badge not bold: %q", out)
	}
}

func TestDiffSplitBands(t *testing.T) {
	tok := activeTheme.Tokens
	out := renderDiffSplit("@@ -1 +1 @@\n-old\n+new", 60)
	if !strings.Contains(out, sgrBg(tok.DiffDeleteGutterBg)) || !strings.Contains(out, sgrBg(tok.DiffInsertGutterBg)) {
		t.Errorf("split view missing band tints: %q", out)
	}
}

// --- quit ------------------------------------------------------------------

func TestQuitDialogButtons(t *testing.T) {
	m := newTestModel(nil)
	m.openQuitDialog()
	if m.quitFocus != quitFocusNope {
		t.Fatal("quit default must be Nope")
	}
	// n stays.
	next, cmd := m.updateQuit(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmdQuits(t, cmd) || next.overlays.has(overlayQuit) {
		t.Fatal("n should close the dialog without quitting")
	}
	// y quits.
	m.openQuitDialog()
	_, cmd = m.updateQuit(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !cmdQuits(t, cmd) {
		t.Fatal("y should quit")
	}
	// Enter on the safe default stays; toggle then Enter quits.
	m.openQuitDialog()
	next, _ = m.updateQuit(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next.overlays.has(overlayQuit) {
		t.Fatal("Enter on Nope should close")
	}
	m.openQuitDialog()
	m.quitFocus = quitFocusYep
	_, cmd = m.updateQuit(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !cmdQuits(t, cmd) {
		t.Fatal("Enter on Yep should quit")
	}
}

// --- themes ------------------------------------------------------------------

func isolateThemeConfig(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
}

func TestThemePickerPreviewAndRevert(t *testing.T) {
	isolateThemeConfig(t)
	defer ActivateTheme("gocode-dark")
	m := newTestModel(nil)
	m.width, m.height = 100, 30
	base := activeTheme.Name
	m.openThemePicker()
	if !m.overlays.has(overlayTheme) {
		t.Fatal("theme picker did not open")
	}
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: tea.KeyDown})
	if activeTheme.Name == base {
		t.Fatal("cursor move did not preview a different theme")
	}
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if activeTheme.Name != base {
		t.Fatalf("esc did not revert: %s != %s", activeTheme.Name, base)
	}
	if m.overlays.has(overlayTheme) {
		t.Fatal("esc did not close the picker")
	}
}

func TestThemePickerEnterPersists(t *testing.T) {
	isolateThemeConfig(t)
	defer ActivateTheme("gocode-dark")
	m := newTestModel(nil)
	m.width, m.height = 100, 30
	m.openThemePicker()
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: tea.KeyDown})
	chosen := activeTheme.Name
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: tea.KeyEnter})
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	if cfg.TUI.ActiveTheme != chosen {
		t.Fatalf("persisted theme = %q, want %q", cfg.TUI.ActiveTheme, chosen)
	}
}

func TestThemeEditorSavesFileBeforeUI(t *testing.T) {
	isolateThemeConfig(t)
	defer ActivateTheme("gocode-dark")
	m := newTestModel(nil)
	m.width, m.height = 100, 30
	m.openThemePicker()
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if !m.overlays.has(overlayThemeEditor) {
		t.Fatal("ctrl+e did not open the editor")
	}
	if m.editor.name != "gocode-dark-custom" {
		t.Fatalf("editor name = %q", m.editor.name)
	}
	m.editor.input.SetValue("#ff0000")
	m, _ = m.updateThemeEditor(tea.KeyPressMsg{Code: tea.KeyEnter})
	path := filepath.Join(themesDir(), "gocode-dark-custom.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("theme file not written: %v", err)
	}
	if activeTheme.Name != "gocode-dark-custom" {
		t.Fatalf("saved theme not applied: %s", activeTheme.Name)
	}
	if hexOf(activeTheme.Tokens.Primary) != "#ff0000" {
		t.Fatalf("edited primary = %s", hexOf(activeTheme.Tokens.Primary))
	}
	if m.overlays.has(overlayThemeEditor) {
		t.Fatal("editor stayed open after save")
	}
}

func TestThemeEditorEscReverts(t *testing.T) {
	isolateThemeConfig(t)
	defer ActivateTheme("gocode-dark")
	m := newTestModel(nil)
	m.width, m.height = 100, 30
	m.openThemePicker()
	m, _ = m.updateThemePicker(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m.editor.input.SetValue("#ff0000")
	m.commitEditorInput()
	if hexOf(activeTheme.Tokens.Primary) != "#ff0000" {
		t.Fatal("editor did not preview live")
	}
	m, _ = m.updateThemeEditor(tea.KeyPressMsg{Code: tea.KeyEsc})
	if activeTheme.Name != "gocode-dark" {
		t.Fatalf("esc did not revert preview: %s", activeTheme.Name)
	}
}

// --- sessions ------------------------------------------------------------------

func TestSessionSwitcherModes(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))
	m, _ = m.updateSessionPicker(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if m.sessionMode != sessionDeleting || m.sessionPicker.Title != "Delete this session?" {
		t.Fatalf("ctrl+x mode = %v title = %q", m.sessionMode, m.sessionPicker.Title)
	}
	m, _ = m.updateSessionPicker(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.sessionMode != sessionNormal {
		t.Fatal("n did not cancel deleting")
	}
	m, _ = m.updateSessionPicker(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if m.sessionMode != sessionRenaming || m.sessionPicker.Title != "Rename this session?" {
		t.Fatalf("ctrl+r mode = %v title = %q", m.sessionMode, m.sessionPicker.Title)
	}
	if m.renameInput.Value() == "" {
		t.Fatal("rename input not seeded with the current title")
	}
	m, _ = m.updateSessionPicker(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.sessionMode != sessionNormal {
		t.Fatal("esc did not cancel renaming")
	}
}

func TestSessionStoreCmdWithoutStore(t *testing.T) {
	m := newTestModel(nil)
	cmd := m.sessionStoreCmd(func(st *session.SessionStore) error { return nil })
	msg := cmd()
	listMsg, ok := msg.(sessionListMsg)
	if !ok || listMsg.err == nil {
		t.Fatalf("missing store should report through sessionListMsg, got %#v", msg)
	}
}

// --- sidebar ------------------------------------------------------------------

func TestSidebarDataSections(t *testing.T) {
	m := newTestModel(nil)
	m.sidebar = SidebarInfo{
		SessionID:    "abc123",
		SessionTitle: "My session",
		MCPServers:   []string{"alpha", "beta", "gamma", "delta"},
	}
	m.planStore = tools.NewPlanStore()
	m.planStore.Replace([]tools.PlanItem{
		{ID: "1", Text: "a", Status: tools.PlanStatusDone},
		{ID: "2", Text: "b", Status: tools.PlanStatusPending},
	})
	out := stripANSI(m.renderSidebar(31, 40))
	for _, want := range []string{"session abc123", "My session", "mcp", "alpha", "+1 more", "plan", "1/2 done"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}
}

func TestReadOnlyBadgeLifecycle(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 100, 30
	m, _ = m.resolveApproval(tools.ApprovalReadOnlyTurn, "✓ Read-only tools allowed this turn", RoleSystem)
	if !m.readOnlyTurn {
		t.Fatal("read-only choice did not set the badge flag")
	}
	if !strings.Contains(stripANSI(m.renderStatusBar()), "READONLY") {
		t.Error("status bar missing the READONLY badge")
	}
	next, _ := m.Update(agentTurnStartMsg{})
	m = next.(Model)
	if m.readOnlyTurn {
		t.Fatal("new turn did not clear the read-only badge")
	}
}

// keep textinput referenced for the rename-input construction path.
var _ textinput.Model
