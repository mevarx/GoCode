package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// settledTranscript builds n distinct rendered messages, the shape a long
// session has after many turns.
func settledTranscript(n int) []ChatMessage {
	msgs := make([]ChatMessage, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, ChatMessage{
			Role:    RoleUser,
			Label:   "You",
			Content: fmt.Sprintf("settled message %d", i),
		})
	}
	return msgs
}

// sameMessageSameWidth: the cached block must be reused verbatim.
func TestRenderCacheReusesUnchangedBlocks(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.messages = settledTranscript(6)

	first := m.renderMessages()
	m.cache.renders = 0
	second := m.renderMessages()

	if m.cache.renders != 0 {
		t.Fatalf("re-rendered %d unchanged blocks; settled history must render once", m.cache.renders)
	}
	if first != second {
		t.Fatal("cached transcript differs from a fresh render")
	}
	if len(m.cache.blocks) != len(m.messages) {
		t.Fatalf("cache holds %d blocks for %d messages", len(m.cache.blocks), len(m.messages))
	}
}

// Every field of ChatMessage is part of the identity: changing any one of them
// must re-render, and only the changed block.
func TestRenderCacheInvalidatesOnAnyFieldChange(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ChatMessage)
	}{
		{"content", func(c *ChatMessage) { c.Content += " changed" }},
		{"label", func(c *ChatMessage) { c.Label = "renamed" }},
		{"diff", func(c *ChatMessage) { c.Diff = "+added\n-removed" }},
		{"role", func(c *ChatMessage) { c.Role = RoleError }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(nil)
			m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			m.messages = settledTranscript(5)
			m.renderMessages()

			m.cache.renders = 0
			tc.mutate(&m.messages[2])
			m.renderMessages()

			if m.cache.renders != 1 {
				t.Fatalf("%s change rendered %d blocks, want exactly the changed one", tc.name, m.cache.renders)
			}
		})
	}
}

// A resize is a width epoch: every block was wrapped for the old width, so all
// of them must render again.
func TestRenderCacheWidthChangeInvalidatesEverything(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.messages = settledTranscript(5)
	m.renderMessages()

	m.cache.renders = 0
	m = drive(m, tea.WindowSizeMsg{Width: 60, Height: 30})

	if m.cache.renders != len(m.messages) {
		t.Fatalf("resize rendered %d of %d blocks", m.cache.renders, len(m.messages))
	}
	if m.cache.width != 60 {
		t.Fatalf("cache epoch = %d, want the new width 60", m.cache.width)
	}
}

// A streaming tool card mutates in place; only that block may re-render.
func TestToolCardMutationInvalidatesOnlyThatBlock(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.messages = []ChatMessage{
		{Role: RoleUser, Label: "You", Content: "read the file"},
		{Role: RoleTool, Label: "🔧 file_read", Content: "running…"},
		{Role: RoleAssistant, Label: "GoCode", Content: "done"},
	}
	m.pendingTools["c2"] = &toolCard{index: 1, since: time.Now()}
	m.renderMessages()

	m.cache.renders = 0
	m.upsertPendingToolCard("file_read", `{"path":"a.go"}`, "c2")
	m.renderMessages()

	if m.cache.renders != 1 {
		t.Fatalf("mutating one tool card rendered %d blocks", m.cache.renders)
	}
	if !strings.Contains(m.messages[1].Content, `{"path":"a.go"}`) {
		t.Errorf("card body not updated: %q", m.messages[1].Content)
	}
}

// The live tail is the only block that may re-render per chunk.
func TestStreamingTailIsTheOnlyRerender(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.messages = settledTranscript(50)

	m.messages = append(m.messages, ChatMessage{Role: RoleAssistant, Label: "GoCode", Content: "first token"})
	m.renderMessages()

	m.cache.renders = 0
	m.messages[len(m.messages)-1].Content = "first token and the rest"
	m.renderMessages()

	if m.cache.renders != 1 {
		t.Fatalf("a chunk re-rendered %d blocks; only the assisted tail may render", m.cache.renders)
	}
}

// Blocks for messages that no longer exist must be dropped so the cache keeps
// one entry per message.
func TestRenderCacheTrimsWithTranscript(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.messages = settledTranscript(5)
	m.renderMessages()

	m.messages = m.messages[:2]
	m.renderMessages()

	if len(m.cache.blocks) != 2 {
		t.Fatalf("cache kept %d blocks for 2 messages", len(m.cache.blocks))
	}
}

// Every View() return path must re-declare the alternate screen and mouse
// tracking: Bubble Tea v2 views are declarative, and coalescing must not have
// introduced a path that forgets them.
func TestViewDeclaresAltScreenAndMouseOnEveryPath(t *testing.T) {
	base := newTestModel(nil)
	base = drive(base, tea.WindowSizeMsg{Width: 90, Height: 24})

	paths := []struct {
		name  string
		setup func(Model) Model
	}{
		{"normal", func(m Model) Model { return m }},
		{"streaming", func(m Model) Model { m.streaming = true; return m }},
		{"approval", func(m Model) Model {
			m.approvalActive = true
			m.approvalReq = ApprovalRequest{ToolName: "file_write", Args: json.RawMessage(`{"path":"a.go"}`)}
			return m
		}},
		{"picker", func(m Model) Model { m.modelPickerActive = true; return m }},
		{"not ready", func(m Model) Model { m.ready = false; return m }},
	}
	for _, p := range paths {
		v := p.setup(base).View()
		if !v.AltScreen {
			t.Errorf("%s: View() dropped AltScreen", p.name)
		}
		if v.MouseMode != tea.MouseModeCellMotion {
			t.Errorf("%s: View() mouse mode = %v, want cell motion", p.name, v.MouseMode)
		}
	}
}

// Chunks mutate the buffer immediately but must not touch the viewport; one
// tick applies all of them in a single update.
func TestChunksCoalesceIntoOneFlush(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.viewportFlushes = 0

	for i := 0; i < 5; i++ {
		m = drive(m, agentChunkMsg{delta: "tok"})
	}

	if m.viewportFlushes != 0 {
		t.Fatalf("5 chunks caused %d viewport updates; streaming must coalesce", m.viewportFlushes)
	}
	if got := m.streamBuf.String(); got != "toktoktoktoktok" {
		t.Fatalf("stream buffer = %q; chunks must append immediately", got)
	}
	if !m.dirty {
		t.Fatal("chunks did not mark the transcript dirty")
	}
	if strings.Contains(stripANSI(m.viewport.GetContent()), "tok") {
		t.Fatal("viewport saw streamed text before the coalescing tick")
	}

	m = drive(m, transcriptFlushMsg{})
	if m.viewportFlushes != 1 {
		t.Fatalf("flush tick applied %d updates, want 1", m.viewportFlushes)
	}
	if !strings.Contains(stripANSI(m.viewport.GetContent()), "toktoktoktoktok") {
		t.Fatal("flush did not apply the buffered chunk text")
	}
	if !strings.Contains(stripANSI(m.View().Content), "toktoktoktoktok") {
		t.Fatal("the flushed transcript did not reach the rendered frame")
	}
	if m.dirty {
		t.Fatal("flush left the transcript dirty")
	}

	// A second tick with nothing new must not re-render or re-apply.
	m = drive(m, transcriptFlushMsg{})
	if m.viewportFlushes != 1 {
		t.Fatalf("idle tick applied %d updates, want 1 total", m.viewportFlushes)
	}
}

// Discrete events are visible in the frame they arrive in, without a tick.
func TestDiscreteEventsFlushImmediately(t *testing.T) {
	events := []struct {
		name string
		msg  tea.Msg
	}{
		{"tool start", agentToolStartMsg{name: "file_write", args: "{}", toolCallID: "c1"}},
		{"tool result", agentToolMsg{name: "file_write", result: "wrote it", toolCallID: "c9"}},
		{"notice", agentToolMsg{name: "GoCode", result: "context compacted"}},
		{"turn end", agentDoneMsg{}},
	}
	for _, ev := range events {
		t.Run(ev.name, func(t *testing.T) {
			m := newTestModel(nil)
			m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
			m = drive(m, agentChunkMsg{delta: "pending tail"})
			before := m.viewportFlushes

			m = drive(m, ev.msg)

			if m.viewportFlushes <= before {
				t.Fatal("discrete event did not flush the viewport immediately")
			}
			if !strings.Contains(stripANSI(m.viewport.GetContent()), "pending tail") {
				t.Error("buffered chunk text was not applied with the discrete event")
			}
		})
	}
}

// A finished tool card replaces its pending card in place and shows the result
// in the same frame, dropping any queued coalescing flush.
func TestToolCardResultFlushesImmediately(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = drive(m, agentToolStartMsg{name: "file_write", args: `{"path":`, toolCallID: "c1"})
	m = drive(m, agentToolDeltaMsg{name: "file_write", args: `{"path":"a.go"}`, toolCallID: "c1"})
	if strings.Contains(stripANSI(m.viewport.GetContent()), "a.go") {
		t.Fatal("tool delta bypassed coalescing")
	}

	m = drive(m, agentToolMsg{name: "file_write", result: "wrote a.go", toolCallID: "c1"})

	if got := stripANSI(m.viewport.GetContent()); !strings.Contains(got, "wrote a.go") {
		t.Errorf("tool result not visible immediately: %q", got)
	}
}

// The transcript keeps following only while the viewport is at the bottom.
func TestStreamingAutoscrollOnlyAtBottom(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 40; i++ {
		m.addMessage(ChatMessage{Role: RoleUser, Label: "You", Content: fmt.Sprintf("history %d", i)})
	}
	m.viewport.GotoBottom()
	if !m.viewport.AtBottom() {
		t.Fatal("test setup: viewport is not at the bottom")
	}

	m = drive(m, agentChunkMsg{delta: "streamed while at the bottom"})
	m = drive(m, transcriptFlushMsg{})
	if !m.viewport.AtBottom() {
		t.Fatal("a viewport parked at the bottom stopped following the stream")
	}

	m.viewport.ScrollUp(5)
	parked := m.viewport.YOffset()
	if m.viewport.AtBottom() {
		t.Fatal("test setup: ScrollUp left the viewport at the bottom")
	}

	m = drive(m, agentChunkMsg{delta: " more streamed text while scrolled up"})
	m = drive(m, transcriptFlushMsg{})
	if got := m.viewport.YOffset(); got != parked {
		t.Fatalf("scrolled-up viewport moved %d→%d; autoscroll must respect the user", parked, got)
	}
}

// The session must idle at zero redraws: no mascot tick is armed at startup.
func TestInitArmsNoIdleTick(t *testing.T) {
	m := newTestModel(nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no commands; output polling would never start")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init returned %T, want a batch of startup commands", msg)
	}
	if len(batch) != 3 {
		t.Fatalf("Init armed %d commands, want 3 (cursor blink, approval poll, output poll)", len(batch))
	}
	if emitsFrameWithin(batch, 20*time.Millisecond) {
		t.Fatal("Init armed a mascot tick; an idle session would redraw forever")
	}
}

// Reduced motion starts no decorative tick at all, while the functional state
// word stays available.
func TestReducedMotionArmsNoAnimationTicks(t *testing.T) {
	old := capabilities.ReducedMotion
	capabilities.ReducedMotion = true
	defer func() { capabilities.ReducedMotion = old }()

	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.textarea.SetValue("hi")

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)

	if m.animating {
		t.Error("reduced motion still marked the mascot as animating")
	}
	if m.mascot.state != mascotThinking {
		t.Error("reduced motion dropped the functional state indicator")
	}
	if emitsFrameWithin([]tea.Cmd{cmd}, 20*time.Millisecond) {
		t.Error("reduced motion armed a mascot tick")
	}
}

// emitsFrameWithin runs cmds concurrently and reports whether any frameMsg
// arrives inside d. Ticks are timer-driven, so a mascot chain would show up in
// tens of milliseconds while blocking channel reads simply time out.
func emitsFrameWithin(cmds []tea.Cmd, d time.Duration) bool {
	ch := make(chan tea.Msg, len(cmds))
	pending := 0
	for _, c := range cmds {
		if c == nil {
			continue
		}
		pending++
		go func(c tea.Cmd) { ch <- c() }(c)
	}
	deadline := time.After(d)
	for pending > 0 {
		select {
		case msg := <-ch:
			pending--
			if batch, ok := msg.(tea.BatchMsg); ok {
				if emitsFrameWithin(batch, d) {
					return true
				}
				continue
			}
			if _, ok := msg.(frameMsg); ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
	return false
}

// Agent traffic must not reach the input box: bubbles' textarea re-renders on
// every message, so forwarding a token stream is pure per-chunk cost.
func TestAgentTrafficSkipsTextarea(t *testing.T) {
	for _, msg := range []tea.Msg{
		agentChunkMsg{delta: "x"},
		agentToolDeltaMsg{name: "t", args: "{}"},
		agentToolStartMsg{name: "t"},
		agentToolMsg{name: "t"},
		agentDoneMsg{},
		agentTurnStartMsg{},
		agentTurnMetricsMsg{},
		approvalRequestMsg{},
		transcriptFlushMsg{},
		frameMsg{},
	} {
		if forwardToTextarea(msg) {
			t.Errorf("%T is forwarded to the textarea", msg)
		}
	}
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'a'}, tea.PasteMsg{}, nil} {
		if !forwardToTextarea(msg) {
			t.Errorf("%T must still reach the textarea", msg)
		}
	}
}

// BenchmarkStreamingChunk measures the steady-state cost of one streamed chunk
// at two transcript lengths. Settled history is already cached, so only the
// live tail renders; the viewport is flushed once per six chunks, matching a
// 30 fps cap against a fast token stream.
func BenchmarkStreamingChunk(b *testing.B) {
	for _, settled := range []int{200, 2000} {
		b.Run(fmt.Sprintf("settled-%d", settled), func(b *testing.B) {
			benchmarkStreamingChunk(b, settled)
		})
	}
}

func benchmarkStreamingChunk(b *testing.B, settled int) {
	m := benchTranscript(b, settled)
	rendersBefore := m.cache.renders

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m = drive(m, agentChunkMsg{delta: "token "})
		if i%6 == 5 {
			m = drive(m, transcriptFlushMsg{})
		}
		// Bound the synthetic live tail: without this a long run measures
		// buffer growth instead of rendering.
		if i%500 == 499 {
			m.streamBuf.Reset()
			m.messages[len(m.messages)-1].Content = ""
		}
	}
	b.StopTimer()

	if b.N > 0 {
		b.ReportMetric(float64(m.cache.renders-rendersBefore)/float64(b.N), "block-renders/chunk")
	}
}

// BenchmarkTranscriptIngest isolates chunk ingestion: buffer append, cache
// marking and command wiring, with no viewport flush. This is the number that
// must not grow with the transcript.
func BenchmarkTranscriptIngest(b *testing.B) {
	for _, settled := range []int{200, 2000} {
		b.Run(fmt.Sprintf("settled-%d", settled), func(b *testing.B) {
			m := benchTranscript(b, settled)
			rendersBefore := m.cache.renders

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m = drive(m, agentChunkMsg{delta: "token "})
				if i%500 == 499 {
					m.streamBuf.Reset()
					m.messages[len(m.messages)-1].Content = ""
				}
			}
			b.StopTimer()

			if b.N > 0 {
				b.ReportMetric(float64(m.cache.renders-rendersBefore)/float64(b.N), "block-renders/chunk")
			}
		})
	}
}

// benchTranscript builds a settled session of n messages with a live assistant
// tail and a warm render cache, the state a streaming turn starts from.
func benchTranscript(b *testing.B, settled int) Model {
	b.Helper()
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.messages = make([]ChatMessage, 0, settled+1)
	for i := 0; i < settled; i++ {
		m.messages = append(m.messages, ChatMessage{
			Role:    RoleUser,
			Label:   "You",
			Content: fmt.Sprintf("settled message %d: %s", i, strings.Repeat("context ", 8)),
		})
	}
	m.messages = append(m.messages, ChatMessage{Role: RoleAssistant, Label: "GoCode", Content: ""})
	m.renderMessages()
	return m
}
