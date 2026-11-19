package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

// --- helpers --------------------------------------------------------------

// openSessionPickerForTest submits a bare /sessions and returns the model with
// the overlay open. The returned command is deliberately not run: callers that
// want the listing feed applySessionList a message themselves.
func openSessionPickerForTest(t *testing.T) Model {
	t.Helper()
	m := p6Model(nil)
	m.textarea.SetValue("/sessions")
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if !m.overlays.has(overlaySession) {
		t.Fatalf("bare /sessions did not open the session picker; stack=%v", m.overlays.kinds)
	}
	return m
}

// withSessions installs two rows through the real message path.
func withSessions(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(sessionListMsg{summaries: []session.SessionSummary{
		{
			ID:           "sess_one",
			Title:        "Fix parser bug",
			Provider:     "ollama",
			Model:        "llama3",
			MessageCount: 12,
			UpdatedAt:    time.Date(2026, 1, 2, 15, 4, 0, 0, time.Local),
		},
		{
			ID:           "sess_two",
			Title:        "Deploy release",
			Provider:     "anthropic",
			Model:        "claude",
			MessageCount: 4,
			UpdatedAt:    time.Date(2026, 1, 3, 9, 30, 0, 0, time.Local),
		},
	}})
	return next.(Model)
}

// runCmd executes a command and feeds its single message back to the model.
// Overlay commands here are all single-shot; a batch would be a bug in the test.
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

// --- session picker -------------------------------------------------------

// A bare /sessions is a UI affordance: it opens the picker, clears the prompt
// and never reaches the engine. With an argument the engine still owns it.
func TestBareSessionsInterceptedOnlyInTUI(t *testing.T) {
	m := openSessionPickerForTest(t)
	if got := m.textarea.Value(); got != "" {
		t.Errorf("prompt not cleared after opening the picker: %q", got)
	}
	if len(m.messages) != 0 {
		t.Errorf("opening the picker wrote %d transcript messages", len(m.messages))
	}
	select {
	case got := <-m.inputCh:
		t.Fatalf("bare /sessions was forwarded to the engine: %q", got)
	default:
	}

	// Arguments and /resume keep going through the engine unchanged.
	for _, input := range []string{"/sessions sess_one", "/resume sess_one", "/SESSIONS sess_one"} {
		m := p6Model(nil)
		m.textarea.SetValue(input)
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = next.(Model)
		if m.overlays.has(overlaySession) {
			t.Fatalf("%q opened the picker; only the bare form may", input)
		}
		select {
		case got := <-m.inputCh:
			if got != input {
				t.Errorf("forwarded %q, want %q", got, input)
			}
		case <-time.After(time.Second):
			t.Fatalf("%q was not forwarded to the engine", input)
		}
	}
}

// Rows carry the same fields the text listing prints: short id, title excerpt,
// updated time and message count.
func TestSessionPickerRowsCarryListingFields(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))

	if len(m.sessionItems) != 2 {
		t.Fatalf("session items = %d, want 2", len(m.sessionItems))
	}
	item, ok := m.sessionPicker.Items()[0].(SessionItem)
	if !ok {
		t.Fatal("picker rows are not SessionItem")
	}
	title := item.Title()
	if !strings.Contains(title, "one") && !strings.Contains(title, "sess_one") {
		t.Errorf("row title lacks the short id: %q", title)
	}
	if !strings.Contains(title, "Fix parser bug") {
		t.Errorf("row title lacks the excerpt: %q", title)
	}
	desc := item.Description()
	for _, want := range []string{"Jan 02 15:04", "12 msgs", "ollama/llama3"} {
		if !strings.Contains(desc, want) {
			t.Errorf("row description lacks %q: %q", want, desc)
		}
	}
	if got := item.FilterValue(); !strings.Contains(got, "sess_one") {
		t.Errorf("filter value cannot match the full id: %q", got)
	}
}

// The short form is the generated id's suffix; ids without that shape stay
// whole so a hand-made record is still recognizable.
func TestSessionIDShort(t *testing.T) {
	if got, want := sessionIDShort("sess_20260102150405_ab12cd34"), "ab12cd34"; got != want {
		t.Errorf("sessionIDShort = %q, want %q", got, want)
	}
	if got, want := sessionIDShort("plain-id"), "plain-id"; got != want {
		t.Errorf("sessionIDShort = %q, want %q", got, want)
	}
}

// Typing narrows the list, Enter accepts the filter, a second Enter resumes the
// highlighted session through the engine's own /resume path.
func TestSessionPickerFilterAndSelectResumes(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))

	m = pressKey(m, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if got := m.sessionPicker.FilterValue(); got != "d" {
		t.Fatalf("filter value = %q, want %q", got, "d")
	}
	visible := m.sessionPicker.VisibleItems()
	if len(visible) != 1 {
		t.Fatalf("filter matched %d rows, want 1", len(visible))
	}
	if item, ok := visible[0].(SessionItem); !ok || item.ID != "sess_two" {
		t.Fatalf("filter matched %+v, want sess_two", visible[0])
	}

	// First Enter accepts the filter, second selects.
	m = pressEnter(m)
	m = pressEnter(m)

	if m.overlays.has(overlaySession) {
		t.Error("selecting a session left the picker open")
	}
	select {
	case got := <-m.inputCh:
		if got != "/resume sess_two" {
			t.Errorf("resume command = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("selecting a session did not send /resume")
	}
}

// Esc closes without resuming anything.
func TestSessionPickerEscCloses(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.overlays.has(overlaySession) {
		t.Fatal("Esc did not close the session picker")
	}
	select {
	case got := <-m.inputCh:
		t.Fatalf("closing the picker forwarded %q", got)
	default:
	}
}

// An empty listing closes the overlay with the engine's wording instead of
// leaving a dead modal on screen.
func TestSessionPickerEmptyListingClosesWithNotice(t *testing.T) {
	m := openSessionPickerForTest(t)
	next, _ := m.Update(sessionListMsg{})
	m = next.(Model)
	if m.overlays.has(overlaySession) {
		t.Error("an empty listing left the picker open")
	}
	if len(m.messages) != 1 || !strings.Contains(m.messages[0].Content, "No saved sessions found.") {
		t.Errorf("missing empty-listing notice: %+v", m.messages)
	}
}

// A store failure is reported in the transcript, not as an empty modal.
func TestSessionPickerStoreErrorFallsBackToNotice(t *testing.T) {
	m := openSessionPickerForTest(t)
	next, _ := m.Update(sessionListMsg{err: errSessionStoreUnavailable})
	m = next.(Model)
	if m.overlays.has(overlaySession) {
		t.Error("a store error left the picker open")
	}
	if len(m.messages) != 1 || !strings.Contains(m.messages[0].Content, "Session store is not enabled.") {
		t.Errorf("missing store-error notice: %+v", m.messages)
	}
}

// The real listing path: /sessions in the TUI reads the same store method the
// engine's text command uses, and the row shows up.
func TestSessionPickerReadsStoreListing(t *testing.T) {
	store, err := session.NewStore(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	if _, err := store.CreateSession("sess_real_ab12cd34", "Real title", "ollama", "llama3"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	m := p6Model(nil)
	m.agentSession = agent.NewSessionWithStore("sess_real_ab12cd34", "ollama", "llama3", store)
	m.textarea.SetValue("/sessions")
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	m = runCmd(t, m, cmd)

	if len(m.sessionItems) != 1 {
		t.Fatalf("picker rows = %d, want 1", len(m.sessionItems))
	}
	item, ok := m.sessionPicker.SelectedItem().(SessionItem)
	if !ok {
		t.Fatal("selected row is not a SessionItem")
	}
	if item.ID != "sess_real_ab12cd34" || item.Excerpt != "Real title" {
		t.Errorf("row = %+v", item)
	}
}

// Approval always wins: a request that lands while the session picker is open
// displaces it, and the modal's keys go to the approval, not the list.
func TestApprovalDisplacesSessionPicker(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))

	next, _ := m.Update(approvalRequestMsg{req: ApprovalRequest{ToolName: "shell_exec"}})
	m = next.(Model)

	if m.overlays.has(overlaySession) {
		t.Fatal("the session picker survived an approval request")
	}
	if !m.overlays.has(overlayApproval) {
		t.Fatal("the approval request did not take focus")
	}
	if kind, _ := m.overlays.top(); kind != overlayApproval {
		t.Fatalf("top overlay = %v, want approval", kind)
	}
	if view := m.View().Content; !strings.Contains(view, "Tool Approval Required") {
		t.Error("the approval modal did not render")
	}

	// Esc in the modal denies; the picker must not come back with it.
	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !m.overlays.empty() {
		t.Fatalf("overlays after denying: %v", m.overlays.kinds)
	}
}

// The overlay renders as a modal without losing the v2 screen contract:
// AltScreen and mouse mode are declared on this path too.
func TestSessionPickerViewKeepsScreenContract(t *testing.T) {
	m := withSessions(t, openSessionPickerForTest(t))
	v := m.View()
	if !v.AltScreen {
		t.Error("AltScreen dropped on the session-picker path")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want cell motion", v.MouseMode)
	}
	if view := stripANSI(v.Content); !strings.Contains(view, "Resume Session") {
		t.Error("the session picker did not render its title")
	}
}

// --- model picker refresh -------------------------------------------------

// refreshProvider serves one batch per Models call, so a second collection can
// observe a model that did not exist at startup.
type refreshProvider struct {
	mu      sync.Mutex
	calls   int
	batches [][]string
}

func (p *refreshProvider) Name() string { return "fake" }

func (p *refreshProvider) Models(context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if len(p.batches) == 0 {
		return nil, errors.New("no models")
	}
	batch := p.batches[0]
	if len(p.batches) > 1 {
		p.batches = p.batches[1:]
	}
	return batch, nil
}

func (p *refreshProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("not implemented")
}

func refreshTestModel(t *testing.T, p *refreshProvider) (Model, *provider.Registry) {
	t.Helper()
	reg := provider.NewRegistry()
	reg.Register(p)
	m := p6Model(nil)
	m.registry = reg
	m.modelPicker.SetItems([]list.Item{
		ModelItem{Provider: "fake", Model: "a"},
		ModelItem{Provider: "fake", Model: "b"},
	})
	m.modelPicker.Select(1)
	return m, reg
}

// Opening the picker re-collects the list, new models appear, and the model the
// user was pointing at stays selected.
func TestModelPickerRefreshOnOpen(t *testing.T) {
	p := &refreshProvider{batches: [][]string{{"a", "b", "c"}}}
	m, _ := refreshTestModel(t, p)

	cmd := m.openModelPicker()
	if cmd == nil {
		t.Fatal("opening the picker did not start a refresh")
	}
	if !m.pickerRefreshInFlight {
		t.Fatal("the refresh was not marked in flight")
	}

	msg, ok := cmd().(pickerRefreshMsg)
	if !ok {
		t.Fatal("the refresh command did not produce a pickerRefreshMsg")
	}
	next, _ := m.Update(msg)
	m = next.(Model)

	if got := len(m.modelPicker.Items()); got != 3 {
		t.Fatalf("picker items = %d, want 3 (refresh did not land)", got)
	}
	sel, ok := m.modelPicker.SelectedItem().(ModelItem)
	if !ok || sel.Model != "b" {
		t.Fatalf("selection = %+v, want the previously selected b", sel)
	}
	if m.pickerRefreshInFlight {
		t.Error("the in-flight latch was not cleared")
	}
}

// A failed or empty collection leaves the snapshot and the selection alone.
func TestModelPickerRefreshFailureKeepsSnapshot(t *testing.T) {
	p := &refreshProvider{batches: [][]string{{"a", "b"}, nil}}
	m, _ := refreshTestModel(t, p)

	// Drain the first refresh so the failure arrives against a known list.
	m = runCmd(t, m, m.openModelPicker())
	before := len(m.modelPicker.Items())

	next, _ := m.Update(pickerRefreshMsg{items: nil})
	m = next.(Model)

	if got := len(m.modelPicker.Items()); got != before {
		t.Errorf("a failed refresh changed the list: %d items, want %d", got, before)
	}
	sel, ok := m.modelPicker.SelectedItem().(ModelItem)
	if !ok || sel.Model != "b" {
		t.Errorf("a failed refresh moved the selection: %+v", sel)
	}
}

// A collection already in flight is never duplicated, and the latch re-opens
// once it lands.
func TestModelPickerRefreshNotDuplicated(t *testing.T) {
	p := &refreshProvider{batches: [][]string{{"a"}, {"a", "b"}}}
	m, _ := refreshTestModel(t, p)

	first := m.openModelPicker()
	if first == nil {
		t.Fatal("first open did not start a refresh")
	}
	if second := m.openModelPicker(); second != nil {
		t.Fatal("a second open started a duplicate refresh")
	}

	m = runCmd(t, m, first)
	if again := m.openModelPicker(); again == nil {
		t.Fatal("a landed refresh did not re-arm the latch")
	}
}

// The shared collection path sees models that appear after startup, bounded by
// the same deadlines.
func TestCollectPickerItemsSeesNewModels(t *testing.T) {
	p := &refreshProvider{batches: [][]string{{"a", "b"}, {"a", "b", "c"}}}
	reg := provider.NewRegistry()
	reg.Register(p)

	first := collectPickerItems(context.Background(), reg)
	second := collectPickerItems(context.Background(), reg)

	if len(first) != 2 || len(second) != 3 {
		t.Fatalf("collections = %d then %d items, want 2 then 3", len(first), len(second))
	}
	found := false
	for _, item := range second {
		if mi, ok := item.(ModelItem); ok && mi.Model == "c" {
			found = true
		}
	}
	if !found {
		t.Error("the refreshed collection did not include the new model")
	}
}

// The palette's /sessions entry opens the picker instead of staging a prompt.
func TestPaletteSessionsEntryOpensPicker(t *testing.T) {
	m := p6Model(nil)
	m = pressKey(m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if !m.overlays.has(overlayPalette) {
		t.Fatalf("Ctrl+K did not open the palette; stack=%v", m.overlays.kinds)
	}
	m.palette.SetFilterText("/sessions")
	m.palette.SetFilterState(list.FilterApplied)
	m = pressEnter(m)
	if !m.overlays.has(overlaySession) {
		t.Fatalf("palette /sessions did not open the picker; stack=%v", m.overlays.kinds)
	}
}

// A refresh that lands while a filter is active restores the selected model
// once the match list is recomputed.
func TestModelPickerRefreshKeepsSelectionUnderFilter(t *testing.T) {
	p := &refreshProvider{batches: [][]string{{"a", "b", "c"}}}
	m, _ := refreshTestModel(t, p)

	// FilterMatchesMsg is routed by the overlay stack, so the picker must be the
	// focused surface, exactly as it is in a real session.
	_ = m.openModelPicker()

	// "f" matches every row through the provider name, so the identity is still
	// present after the filter is applied.
	m.modelPicker.SetFilterText("f")
	m.modelPicker.SetFilterState(list.FilterApplied)
	// Filtering moves the cursor to the start; point it back at "b" so the
	// refresh has a selection worth preserving.
	m.modelPicker.Select(1)
	if sel, ok := m.modelPicker.SelectedItem().(ModelItem); !ok || sel.Model != "b" {
		t.Fatalf("setup selection = %+v, want b", m.modelPicker.SelectedItem())
	}

	items := []list.Item{
		ModelItem{Provider: "fake", Model: "a"},
		ModelItem{Provider: "fake", Model: "b"},
		ModelItem{Provider: "fake", Model: "c"},
	}
	cmd := m.applyPickerRefresh(items)
	if m.pickerKeep == nil {
		t.Fatal("a filtered refresh did not park the selection")
	}
	m = runCmd(t, m, cmd)

	sel, ok := m.modelPicker.SelectedItem().(ModelItem)
	if !ok || sel.Model != "b" {
		t.Fatalf("selection = %+v, want b after the filtered refresh", sel)
	}
	if m.pickerKeep != nil {
		t.Error("the parked selection was not released")
	}
}

// --- tool card lifecycle --------------------------------------------------

// pending → running → done, with the ✓ and a rendered diff on the settled card.
func TestToolCardStatusTransitions(t *testing.T) {
	forceMotionAll(t)
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true

	// Arguments stream in: the card is pending and owns exactly one block.
	m = drive(m, agentToolDeltaMsg{name: "file_write", args: `{"path":`, toolCallID: "c1"})
	m = revealAll(t, m)
	if got := m.messages[0].Content; !strings.Contains(got, "pending…") {
		t.Errorf("delta card = %q, want the pending phase", got)
	}
	if got := m.messages[0].Label; got != "🔧 file_write" {
		t.Errorf("pending label = %q", got)
	}

	// The engine announces the call: running.
	m = drive(m, agentToolStartMsg{name: "file_write", args: `{"path":"a.go"}`, toolCallID: "c1"})
	m = revealAll(t, m)
	if got := m.messages[0].Content; !strings.Contains(got, "running…") {
		t.Errorf("started card = %q, want the running phase", got)
	}
	if len(m.messages) != 1 {
		t.Fatalf("start duplicated the card: %d messages", len(m.messages))
	}

	// The result settles the card as done, keeping the diff.
	m = drive(m, agentToolMsg{
		name:       "file_write",
		result:     "wrote 3 lines",
		toolCallID: "c1",
		diff:       "@@ -1 +1 @@\n-old\n+new",
	})
	if got := m.messages[0].Label; got != toolDoneGlyph+" file_write" {
		t.Errorf("done label = %q", got)
	}
	if !m.messages[0].Done {
		t.Error("done card is not marked Done")
	}
	if _, ok := m.pendingTools["c1"]; ok {
		t.Error("the done card is still tracked as pending")
	}
	rendered := stripANSI(m.renderMessage(m.messages[0]))
	for _, want := range []string{"✓ file_write", "wrote 3 lines", "+new"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("settled card render lacks %q:\n%s", want, rendered)
		}
	}
}

// A failed call becomes the error role: ✗ plus the danger style, not a ✓.
func TestToolCardErrorTransition(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true

	m = drive(m, agentToolStartMsg{name: "file_write", args: `{"path":"a.go"}`, toolCallID: "c2"})
	m = drive(m, agentToolMsg{name: "file_write", result: "permission denied", isError: true, toolCallID: "c2"})

	if got := m.messages[0].Role; got != RoleError {
		t.Fatalf("failed card role = %v, want RoleError", got)
	}
	if m.messages[0].Done {
		t.Error("a failed card must not carry the done flag")
	}
	rendered := stripANSI(m.renderMessage(m.messages[0]))
	if !strings.Contains(rendered, toolErrorGlyph+" file_write") {
		t.Errorf("error card render lacks the ✗ label: %q", rendered)
	}
	if strings.Contains(rendered, toolDoneGlyph) {
		t.Errorf("error card rendered a ✓: %q", rendered)
	}
	// The error role is the danger style; the done role is the success style.
	if toolDoneLabelStyle.GetForeground() != colorSuccess {
		t.Error("done label does not use the success palette role")
	}
	if errorStyle.GetForeground() != colorDanger {
		t.Error("error style does not use the danger palette role")
	}
}

// The card composer keeps the 200-rune cap through every phase.
func TestToolCardStatusKeepsPreviewCap(t *testing.T) {
	long := strings.Repeat("x", 500)
	for _, status := range []toolCardStatus{toolCardPending, toolCardRunning} {
		got := toolCardContent(status, "⠋", 0, long)
		_, body, ok := strings.Cut(got, "\n")
		if !ok {
			t.Fatalf("status %v produced no preview line: %q", status, got)
		}
		if len(body) != toolPreviewRunes {
			t.Errorf("status %v preview = %d bytes, want %d", status, len(body), toolPreviewRunes)
		}
	}
}

// --- steering chip --------------------------------------------------------

// One queued message keeps the sentence; a batch shows the count.
func TestSteerChipCount(t *testing.T) {
	m := newTestModel(nil)

	m.queuedSteer = 1
	if chip := stripANSI(m.renderSteerChip()); !strings.Contains(chip, "1 steered message queued") {
		t.Errorf("single chip = %q", chip)
	}

	m.queuedSteer = 3
	chip := stripANSI(m.renderSteerChip())
	if !strings.Contains(chip, "3 queued") {
		t.Errorf("batch chip = %q, want the count", chip)
	}
	if strings.Contains(chip, "steered messages") {
		t.Errorf("batch chip still uses the sentence form: %q", chip)
	}
}
