package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestClippedToolOutputDoesNotLeakBackground(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	item := m.renderChatItem(ChatMessage{Role: RoleTool, Label: "GoCode", Content: "Active Provider: ollama\nAvailable Providers: " + strings.Repeat("provider, ", 30)}, false)
	last := strings.Split(item, "\n")[2]
	if !strings.Contains(last, "\x1b[") {
		t.Fatalf("fixture lost its style: %q", last)
	}
	if !strings.HasSuffix(last, "\x1b[m") && !strings.HasSuffix(last, "\x1b[0m") {
		t.Fatalf("truncated tool output leaves its background active: %q", last)
	}
}

func TestHorizontalTranscriptScrollUsesStyledCells(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	m.hOffset = map[int]int{0: 5}
	msg := ChatMessage{Role: RoleError, Label: "GoCode", Content: "long error: 日🙂abc " + strings.Repeat("tail ", 30)}
	base := stripANSI(m.renderMessage(msg))
	// The prefix before GoCode is two cells; five cells into the line lands on 'o'.
	got := m.renderChatItemView(0, msg, false)
	if !strings.HasPrefix(stripANSI(got), "  ode: long error:") || !strings.Contains(got, "\x1b[") {
		t.Fatalf("horizontal scroll did not preserve text/style: base=%q got=%q", base, got)
	}
	if lipgloss.Width(got) > m.chatContentWidth() {
		t.Fatalf("scrolled item exceeds transcript width: %q", got)
	}
}

func TestRenderedFooterShowsTranscriptKeysAndCompletion(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 140, Height: 40})
	m.workspaceRoot = "workspace"
	for _, want := range []string{"PgUp/PgDn scroll", "Tab chat", "workspace"} {
		if !strings.Contains(stripANSI(m.View().Content), want) {
			t.Errorf("rendered frame hides %q", want)
		}
	}
	m.completionHint = "/compact  /clear"
	if !strings.Contains(stripANSI(m.View().Content), m.completionHint) {
		t.Error("rendered frame hides the active command completion")
	}
}

func TestGoCodeReportsWrapInsteadOfHidingProviderNames(t *testing.T) {
	for _, isError := range []bool{false, true} {
		m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
		m = drive(m, agentToolMsg{name: "GoCode", isError: isError,
			result: "Available providers: " + strings.Repeat("another-provider, ", 8) + "opencode-go"})
		if got := stripANSI(m.View().Content); !strings.Contains(got, "opencode-go") {
			t.Errorf("the last provider is clipped (error=%t): %q", isError, got)
		}
	}
}

func TestSettledAssistantTailCollapsesWithoutAnotherMessage(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	m.streaming = true
	text := strings.Repeat("paragraph\n\n", 18) + "last paragraph"
	m = drive(m, agentChunkMsg{delta: text})
	m = drive(m, animTickMsg{at: msgNow(), epoch: m.animEpoch, seq: m.clockSeq})
	if strings.Contains(stripANSI(m.viewport.GetContent()), "space to expand") {
		t.Fatal("the live response must not be collapsed")
	}
	m = drive(m, agentDoneMsg{})
	if !strings.Contains(stripANSI(m.viewport.GetContent()), "space to expand") {
		t.Fatal("the transcript reused the uncollapsed live render after the turn ended")
	}
}

func TestToolCardSeparatesAssistantStreamingBlocks(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	m.streaming = true
	m.motion = MotionSettings{Mode: MotionOff}
	m = drive(m, agentChunkMsg{delta: "before the tool"})
	m = drive(m, agentToolDeltaMsg{name: "read", toolCallID: "call", args: "{}"})
	m = drive(m, agentToolStartMsg{name: "read", toolCallID: "call", args: "{}"})
	m = drive(m, agentToolMsg{name: "read", toolCallID: "call", result: "tool output"})
	m = drive(m, agentChunkMsg{delta: "after the tool"})
	m = drive(m, agentDoneMsg{})
	if len(m.messages) != 3 || m.messages[0].Content != "before the tool" ||
		m.messages[1].Content != "tool output" || m.messages[2].Content != "after the tool" {
		t.Fatalf("assistant text was lost or replayed across the tool card: %+v", m.messages)
	}
}

func TestPromptAndTranscriptRepaintOnThemePreview(t *testing.T) {
	restoreTheme(t)
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 80, Height: 25})
	m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: "example"})
	before := m.viewport.GetContent()
	m.openThemePicker()
	for i, item := range m.themeItems {
		if item.(themeItem).name == LightThemeName {
			m.themePicker.Select(i)
			break
		}
	}
	m.previewThemeSelection()
	if activeTheme.Name != LightThemeName || m.viewport.GetContent() == before {
		t.Error("theme preview left the transcript painted in the old theme")
	}
	prompt := m.renderInputArea()
	if !strings.Contains(prompt, sgrFg(activeTheme.Tokens.FgSubtle)) || strings.Contains(prompt, "\x1b[40m") {
		t.Errorf("prompt still uses the default black textarea colors: %q", prompt)
	}
	if !strings.HasPrefix(stripANSI(prompt), "::: Type your message") {
		t.Error("textarea still paints a second prompt inside the GoCode prompt")
	}
}

func TestSidebarRendersOneWordmark(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 140, Height: 40})
	if got := strings.Count(stripANSI(m.renderSidebar(31, 33)), "GoCode"); got != 1 {
		t.Fatalf("sidebar repeats its wordmark %d times", got)
	}
}

func TestLongModelNamesDoNotDisplaceScrollHelp(t *testing.T) {
	m := drive(newTestModel(nil), tea.WindowSizeMsg{Width: 100, Height: 30})
	m.modelName = "hf.co/" + strings.Repeat("long-model-name-", 14)
	m.workspaceRoot = "workspace"
	if !strings.Contains(stripANSI(m.View().Content), "PgUp/PgDn scroll · Tab chat") {
		t.Fatal("the model id crowded out the scroll and chat shortcuts")
	}
}
