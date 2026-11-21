package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/provider"
)

// p6Model builds a sized model with optional picker items and no registry.
func p6Model(items []list.Item) Model {
	m := NewModel("ollama", "llama3", "0.4.0", "", NewApprovalBridge(),
		make(chan string, 8), make(chan string, 8), make(chan tea.Msg, 32),
		make(chan struct{}, 1), items, testMotion())
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func pressKey(m Model, msg tea.KeyPressMsg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func pressTab(m Model) Model {
	return pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
}

func pressEnter(m Model) Model {
	return pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func pressAlt(m Model, code rune) Model {
	return pressKey(m, tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
}

// TestKeymapCoversEveryBinding is the keymap contract: every legacy binding
// from the audit table keeps its key, context and action, and the P6 additions
// are present with non-colliding contexts.
func TestKeymapCoversEveryBinding(t *testing.T) {
	want := []struct {
		key    string
		ctx    keyContext
		action keyAction
	}{
		// Legacy bindings.
		{"ctrl+l", keyCtxGlobal, actOpenPicker}, // works while streaming too, as before
		{"ctrl+c", keyCtxStreaming, actCancelOrQuit},
		{"ctrl+c", keyCtxIdle, actQuit},
		{"esc", keyCtxStreaming, actCancelOrQuit},
		{"esc", keyCtxIdle, actClearInput},
		{"enter", keyCtxIdle, actSubmit},
		{"enter", keyCtxStreaming, actSteer},
		{"shift+enter", keyCtxTextarea, actNewline},
		{"up", keyCtxTranscript, actScrollUp},
		{"down", keyCtxTranscript, actScrollDown},
		{"pgup", keyCtxGlobal, actScrollPageUp},
		{"pgdown", keyCtxGlobal, actScrollPageDn},
		{"left", keyCtxApproval, actApprovalToggle},
		{"right", keyCtxApproval, actApprovalToggle},
		{"tab", keyCtxApproval, actApprovalToggle},
		{"enter", keyCtxApproval, actApprovalConfirm},
		{"y", keyCtxApproval, actApprovalAllow},
		{"n", keyCtxApproval, actApprovalReject},
		{"a", keyCtxApproval, actApprovalGrant},
		{"r", keyCtxApproval, actApprovalReadOnly},
		{"esc", keyCtxApproval, actApprovalReject},
		{"ctrl+c", keyCtxApproval, actApprovalDenyQuit},
		{"ctrl+l", keyCtxPicker, actCloseOverlay},
		{"esc", keyCtxPicker, actCloseOverlay},
		{"enter", keyCtxPicker, actPickerSelect},

		// P6 additions.
		{"tab", keyCtxIdle, actComplete},
		{"alt+p", keyCtxIdle, actHistoryPrev},
		{"alt+n", keyCtxIdle, actHistoryNext},
		{"up", keyCtxIdle, actRecallPrev},
		{"ctrl+k", keyCtxGlobal, actPaletteOpen},
		{"ctrl+k", keyCtxPalette, actPaletteClose},
		{"esc", keyCtxPalette, actPaletteClose},
		{"enter", keyCtxPalette, actPaletteSelect},
		{"up", keyCtxPalette, actPaletteUp},
		{"down", keyCtxPalette, actPaletteDown},
	}
	for _, w := range want {
		b, ok := lookupBinding(w.ctx, w.key)
		if !ok {
			t.Errorf("missing binding %q in context %d", w.key, w.ctx)
			continue
		}
		if b.Action != w.action {
			t.Errorf("binding %q context %d action = %q, want %q", w.key, w.ctx, b.Action, w.action)
		}
	}

	// Approval owns Tab: the completion binding must not shadow the modal's.
	if b, _ := lookupBinding(keyCtxApproval, "tab"); b.Action != actApprovalToggle {
		t.Fatalf("approval Tab action = %q, want toggle", b.Action)
	}
	if b, _ := lookupBinding(keyCtxIdle, "tab"); b.Action != actComplete {
		t.Fatalf("idle Tab action = %q, want complete", b.Action)
	}
}

// TestHelpLineRendersFromKeymap checks the hints come from the table and that
// the palette hint appears at full width without dropping existing hints.
func TestHelpLineRendersFromKeymap(t *testing.T) {
	m := p6Model(nil)
	line := stripANSI(m.renderHelpLine())
	// the palette hint shows the canonical ctrl+p; ctrl+k stays a silent alias.
	for _, want := range []string{"Enter send", "Shift+Enter newline", "Ctrl+P commands", "Ctrl+L models", "Esc clear", "Ctrl+C quit", "PgUp/PgDn scroll"} {
		if !strings.Contains(line, want) {
			t.Errorf("help line missing %q: %q", want, line)
		}
	}

	m.streaming = true
	if got := stripANSI(m.renderHelpLine()); !strings.Contains(got, "Ctrl+C stop") {
		t.Errorf("streaming help line missing stop hint: %q", got)
	}
	m.cancelRequested = true
	if got := stripANSI(m.renderHelpLine()); !strings.Contains(got, "Ctrl+C again to quit") {
		t.Errorf("cancel-requested help line wrong: %q", got)
	}

	m.completionHint = "Tab: /compact  /commit"
	if got := stripANSI(m.renderHelpLine()); !strings.Contains(got, "/compact") {
		t.Errorf("completion hint did not take the help line: %q", got)
	}
}

// TestTabCompletionCommands covers unique, ambiguous, cyclic and no-op cases
// for command-name completion.
func TestTabCompletionCommands(t *testing.T) {
	t.Run("unique completes with space", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("/hel")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/help " {
			t.Fatalf("value = %q, want %q", got, "/help ")
		}
		if m.completionHint != "" {
			t.Fatalf("unique completion left a candidate hint: %q", m.completionHint)
		}
	})

	t.Run("ambiguous extends to common prefix and lists candidates", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("/co")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/com" {
			t.Fatalf("value = %q, want %q", got, "/com")
		}
		if !strings.Contains(m.completionHint, "/compact") || !strings.Contains(m.completionHint, "/commit") {
			t.Fatalf("hint missing candidates: %q", m.completionHint)
		}
		if !strings.Contains(stripANSI(m.renderHelpLine()), "/compact") {
			t.Fatalf("candidate hint did not reach the help line")
		}
	})

	t.Run("repeated tab cycles candidates", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("/co")
		m = pressTab(m)
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/compact " {
			t.Fatalf("first cycle = %q, want %q", got, "/compact ")
		}
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/commit " {
			t.Fatalf("second cycle = %q, want %q", got, "/commit ")
		}
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/compact " {
			t.Fatalf("cycle did not wrap: %q", got)
		}
	})

	t.Run("no match leaves value alone", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("/zzz")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/zzz" {
			t.Fatalf("value = %q, want unchanged", got)
		}
	})

	t.Run("non slash text is untouched", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("hello")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "hello" {
			t.Fatalf("value = %q, want unchanged", got)
		}
	})

	t.Run("second argument is free text", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("/commit fix the bug")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/commit fix the bug" {
			t.Fatalf("value = %q, want unchanged", got)
		}
	})
}

// TestTabCompletionArguments covers /model (picker snapshot) and /provider
// (live registry names).
func TestTabCompletionArguments(t *testing.T) {
	items := []list.Item{
		ModelItem{Provider: "ollama", Model: "gpt-4o"},
		ModelItem{Provider: "ollama", Model: "gpt-4o-mini"},
		ModelItem{Provider: "other", Model: "gpt-4o-other"},
	}

	t.Run("model unique", func(t *testing.T) {
		m := p6Model(items)
		m.textarea.SetValue("/model gpt-4o-m")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/model gpt-4o-mini " {
			t.Fatalf("value = %q", got)
		}
	})

	t.Run("model ambiguous then cycles", func(t *testing.T) {
		m := p6Model(items)
		m.textarea.SetValue("/model gpt-4o")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/model gpt-4o" {
			t.Fatalf("value = %q, want common prefix unchanged", got)
		}
		if !strings.Contains(m.completionHint, "gpt-4o-mini") {
			t.Fatalf("hint missing second model: %q", m.completionHint)
		}
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/model gpt-4o " {
			t.Fatalf("cycle 1 = %q", got)
		}
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/model gpt-4o-mini " {
			t.Fatalf("cycle 2 = %q", got)
		}
	})

	t.Run("model ignores other providers", func(t *testing.T) {
		m := p6Model(items)
		m.textarea.SetValue("/model other")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/model other" {
			t.Fatalf("completed across providers: %q", got)
		}
	})

	t.Run("provider unique", func(t *testing.T) {
		reg := provider.NewRegistry()
		reg.Register(&fakeProvider{name: "alpha"})
		reg.Register(&fakeProvider{name: "beta"})
		m := p6Model(nil)
		m.registry = reg
		m.textarea.SetValue("/provider be")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/provider beta " {
			t.Fatalf("value = %q", got)
		}
	})

	t.Run("provider empty argument lists all", func(t *testing.T) {
		reg := provider.NewRegistry()
		reg.Register(&fakeProvider{name: "alpha"})
		reg.Register(&fakeProvider{name: "beta"})
		m := p6Model(nil)
		m.registry = reg
		m.textarea.SetValue("/provider ")
		m = pressTab(m)
		if got := m.textarea.Value(); got != "/provider " {
			t.Fatalf("value = %q, want unchanged", got)
		}
		if !strings.Contains(m.completionHint, "alpha") || !strings.Contains(m.completionHint, "beta") {
			t.Fatalf("hint missing providers: %q", m.completionHint)
		}
	})
}

// TestTabInApprovalTogglesFocusNotCompletion guards the modal contract: Tab
// must keep switching Approve/Deny and must not touch the prompt.
func TestTabInApprovalTogglesFocusNotCompletion(t *testing.T) {
	m := p6Model(nil)
	m.overlays.push(overlayApproval)
	m.approvalReq = ApprovalRequest{ToolName: "file_write", Args: json.RawMessage(`{}`)}
	m.textarea.SetValue("/hel")

	m = pressTab(m)
	if m.approvalFocus != 1 {
		t.Fatalf("approval focus = %d, want 1", m.approvalFocus)
	}
	if got := m.textarea.Value(); got != "/hel" {
		t.Fatalf("approval Tab edited the prompt: %q", got)
	}
	m = pressTab(m)
	if m.approvalFocus != 0 {
		t.Fatalf("approval focus did not toggle back: %d", m.approvalFocus)
	}
}

// TestPromptHistory covers recall order, dedup, the draft restore, and the
// empty-Up rule.
func TestPromptHistory(t *testing.T) {
	t.Run("submission records and dedups", func(t *testing.T) {
		m := p6Model(nil)
		m.textarea.SetValue("first prompt")
		m = pressEnter(m)
		if len(m.promptHistory.entries) != 1 || m.promptHistory.entries[0] != "first prompt" {
			t.Fatalf("history = %v", m.promptHistory.entries)
		}
		m = drive(m, agentDoneMsg{})
		m.textarea.SetValue("second prompt")
		m = pressEnter(m)
		m = drive(m, agentDoneMsg{})
		if len(m.promptHistory.entries) != 2 {
			t.Fatalf("history = %v, want 2 entries", m.promptHistory.entries)
		}

		m.promptHistory.add("second prompt")
		if len(m.promptHistory.entries) != 2 {
			t.Fatalf("consecutive duplicate recorded: %v", m.promptHistory.entries)
		}

		// Only consecutive duplicates collapse; a returning prompt is new.
		m.promptHistory.add("first prompt")
		if len(m.promptHistory.entries) != 3 {
			t.Fatalf("non-consecutive repeat was dropped: %v", m.promptHistory.entries)
		}
	})

	t.Run("alt p and alt n walk the list", func(t *testing.T) {
		m := p6Model(nil)
		m.promptHistory.add("one")
		m.promptHistory.add("two")

		m = pressAlt(m, 'p')
		if got := m.textarea.Value(); got != "two" {
			t.Fatalf("Alt+P value = %q, want newest", got)
		}
		m = pressAlt(m, 'p')
		if got := m.textarea.Value(); got != "one" {
			t.Fatalf("Alt+P value = %q, want oldest", got)
		}
		m = pressAlt(m, 'p')
		if got := m.textarea.Value(); got != "one" {
			t.Fatalf("Alt+P past the oldest changed value to %q", got)
		}
		m = pressAlt(m, 'n')
		if got := m.textarea.Value(); got != "two" {
			t.Fatalf("Alt+N value = %q, want two", got)
		}
		m = pressAlt(m, 'n')
		if got := m.textarea.Value(); got != "" {
			t.Fatalf("Alt+N at the end restored %q, want the empty draft", got)
		}
	})

	t.Run("up recalls only from empty focused prompt", func(t *testing.T) {
		m := p6Model(nil)
		m.promptHistory.add("one")
		m.promptHistory.add("two")

		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		if got := m.textarea.Value(); got != "two" {
			t.Fatalf("empty Up value = %q, want newest", got)
		}

		m.textarea.SetValue("draft text")
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		if got := m.textarea.Value(); got != "draft text" {
			t.Fatalf("Up with content changed the prompt to %q", got)
		}

		m.textarea.Reset()
		m.textarea.Blur()
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyUp})
		if got := m.textarea.Value(); got != "" {
			t.Fatalf("unfocused Up recalled history into the prompt: %q", got)
		}
	})

	t.Run("steering submissions are recorded", func(t *testing.T) {
		m := p6Model(nil)
		m.streaming = true
		m.textarea.SetValue("steer this")
		m = pressEnter(m)
		if len(m.promptHistory.entries) != 1 || m.promptHistory.entries[0] != "steer this" {
			t.Fatalf("steering not recorded: %v", m.promptHistory.entries)
		}
		if m.queuedSteer != 1 {
			t.Fatalf("queuedSteer = %d, want 1", m.queuedSteer)
		}
		select {
		case got := <-m.steerCh:
			if got != "steer this" {
				t.Fatalf("steerCh got %q", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("steerCh never received the prompt")
		}
	})
}

// TestCommandPalette covers open, filter, select, staging and close.
func TestCommandPalette(t *testing.T) {
	ctrlK := tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}

	t.Run("ctrl+k opens a registry-backed list", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		if !m.overlays.has(overlayPalette) {
			t.Fatal("Ctrl+K did not open the palette")
		}
		view := stripANSI(m.View().Content)
		for _, want := range []string{"Commands", "/help", "Show this help message"} {
			if !strings.Contains(view, want) {
				t.Errorf("palette view missing %q", want)
			}
		}
	})

	t.Run("typing filters and enter runs", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		m = pressKey(m, tea.KeyPressMsg{Code: 'h', Text: "h"})
		if !m.palette.SettingFilter() || m.palette.FilterValue() != "h" {
			t.Fatalf("typing did not start the filter: state=%v value=%q", m.palette.FilterState(), m.palette.FilterValue())
		}
		if got := len(m.palette.VisibleItems()); got >= len(m.paletteItems) {
			t.Fatalf("filter 'h' matched %d commands; the fuzzy filter did not narrow", got)
		}

		// A complete query is deterministic; the fuzzy scorer ranks /help first.
		m.palette.SetFilterText("help")
		if sel := m.palette.SelectedItem(); sel == nil || sel.(commandItem).Name != "/help" {
			t.Fatalf("selected item = %v, want /help", sel)
		}

		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		if m.overlays.has(overlayPalette) {
			t.Fatal("palette stayed open after Enter")
		}
		select {
		case got := <-m.inputCh:
			if got != "/help" {
				t.Fatalf("executed %q, want /help", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("selected command never reached inputCh")
		}
	})

	t.Run("argument commands stage at the prompt", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		m.palette.SetFilterText("model")
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		if m.overlays.has(overlayPalette) {
			t.Fatal("palette stayed open after selecting /model")
		}
		if got := m.textarea.Value(); got != "/model " {
			t.Fatalf("staged value = %q, want %q", got, "/model ")
		}
		select {
		case got := <-m.inputCh:
			t.Fatalf("argument command ran immediately: %q", got)
		default:
		}
	})

	t.Run("async filter matches are applied", func(t *testing.T) {
		// After the first typed rune seeds the filter, bubbles returns filter
		// commands asynchronously; the model must feed their matches back to
		// the list or the visible rows never narrow.
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		m = pressKey(m, tea.KeyPressMsg{Code: 'm', Text: "m"})
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
		m = next.(Model)
		m = runFilterCommands(m, cmd, 0)
		if got := m.palette.FilterValue(); got != "mo" {
			t.Fatalf("filter value = %q, want mo", got)
		}
		sel := m.palette.SelectedItem()
		if sel == nil {
			t.Fatal("no match selected after the async filter applied")
		}
		if name := sel.(commandItem).Name; name != "/model" {
			t.Fatalf("selected %q after filtering 'mo', want /model", name)
		}
	})

	t.Run("esc and ctrl+k close", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.overlays.has(overlayPalette) {
			t.Fatal("Esc did not close the palette")
		}
		m = pressKey(m, ctrlK)
		m = pressKey(m, ctrlK)
		if m.overlays.has(overlayPalette) {
			t.Fatal("Ctrl+K did not toggle the palette closed")
		}
	})

	t.Run("no-argument exit runs through inputCh", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, ctrlK)
		m.palette.SetFilterText("exit")
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		select {
		case got := <-m.inputCh:
			if got != "/exit" {
				t.Fatalf("executed %q, want /exit", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("palette never sent /exit")
		}
	})
}

// runFilterCommands executes a command (recursing through batches) and feeds
// any FilterMatchesMsg back into the model, mirroring the Bubble Tea runtime.
func runFilterCommands(m Model, cmd tea.Cmd, depth int) Model {
	if cmd == nil || depth > 6 {
		return m
	}
	switch out := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range out {
			m = runFilterCommands(m, c, depth+1)
		}
	case list.FilterMatchesMsg:
		next, _ := m.Update(out)
		m = next.(Model)
	}
	return m
}

// TestOverlayPriority: an incoming approval always wins focus, displacing the
// palette or picker, and its keys keep working afterwards.
func TestOverlayPriority(t *testing.T) {
	newApproval := func() approvalRequestMsg {
		return approvalRequestMsg{req: ApprovalRequest{
			ToolName: "shell_exec",
			Args:     json.RawMessage(`{"command":"ls"}`),
			Preview:  "run ls?",
			ReplyCh:  make(chan bool, 1),
		}}
	}

	for _, open := range []struct {
		name string
		do   func(Model) Model
	}{
		{"palette", func(m Model) Model { return pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}) }},
		{"picker", func(m Model) Model { return pressKey(m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}) }},
	} {
		t.Run(open.name+" displaced by approval", func(t *testing.T) {
			m := p6Model(nil)
			m = open.do(m)
			if m.overlays.empty() {
				t.Fatal("overlay did not open")
			}

			next, _ := m.Update(newApproval())
			m = next.(Model)
			if !m.overlays.has(overlayApproval) {
				t.Fatal("approval did not take focus")
			}
			if m.overlays.has(overlayPalette) || m.overlays.has(overlayPicker) {
				t.Fatal("approval did not displace the lower overlay")
			}
			if !strings.Contains(stripANSI(m.View().Content), "shell_exec") {
				t.Fatal("approval modal is not rendered")
			}
			m.grace.skip() // decision-flow test; grace timing has dedicated tests

			// Modal keys still work: Tab focuses Deny, y approves.
			m = pressTab(m)
			if m.approvalFocus != 1 {
				t.Fatalf("approval focus = %d after Tab", m.approvalFocus)
			}
			next, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
			m = next.(Model)
			if m.overlays.has(overlayApproval) {
				t.Fatal("approval stayed open after y")
			}
		})
	}

	t.Run("approval reply reaches the requester", func(t *testing.T) {
		m := p6Model(nil)
		m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
		msg := newApproval()
		next, _ := m.Update(msg)
		m = next.(Model)
		m.grace.skip() // decision-flow test; grace timing has dedicated tests
		next, _ = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
		_ = next.(Model)
		select {
		case ok := <-msg.req.ReplyCh:
			if !ok {
				t.Fatal("y replied false")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("approval reply never arrived")
		}
	})

	t.Run("palette cannot open over approval", func(t *testing.T) {
		m := p6Model(nil)
		next, _ := m.Update(newApproval())
		m = next.(Model)
		m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
		if m.overlays.has(overlayPalette) {
			t.Fatal("palette opened while an approval was pending")
		}
	})
}

// TestOverlayStackApprovalWins is the unit-level contract for the stack.
func TestOverlayStackApprovalWins(t *testing.T) {
	var s overlayStack
	s.push(overlayPalette)
	s.push(overlayPicker)
	s.push(overlayApproval)
	if !s.has(overlayApproval) || s.has(overlayPalette) || s.has(overlayPicker) {
		t.Fatalf("approval push did not clear the stack: %v", s.kinds)
	}
	s.push(overlayPalette)
	if s.has(overlayPalette) {
		t.Fatal("palette opened on top of an approval")
	}
	s.pop(overlayApproval)
	if !s.empty() {
		t.Fatalf("stack not empty after approval closed: %v", s.kinds)
	}
}

// TestPaletteDeclaresAltScreenAndMouse keeps the v2 view contract on the new
// overlay path.
func TestPaletteDeclaresAltScreenAndMouse(t *testing.T) {
	m := p6Model(nil)
	m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	v := m.View()
	if !v.AltScreen {
		t.Error("palette view dropped AltScreen")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("palette view mouse mode = %v", v.MouseMode)
	}
}

// TestPaletteOpenWhileStreaming documents that the palette may open mid-turn;
// execution follows the existing slash semantics.
func TestPaletteOpenWhileStreaming(t *testing.T) {
	m := p6Model(nil)
	m.streaming = true
	m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if !m.overlays.has(overlayPalette) {
		t.Fatal("palette did not open while streaming")
	}
}

// TestExitStillQuitsByTyping preserves the pre-P6 bare-alias behaviour.
func TestExitStillQuitsByTyping(t *testing.T) {
	for _, alias := range []string{"exit", "quit", "/exit", "/quit"} {
		m := p6Model(nil)
		m.textarea.SetValue(alias)
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if !cmdQuits(t, cmd) {
			t.Errorf("%q no longer quits", alias)
		}
	}
}
