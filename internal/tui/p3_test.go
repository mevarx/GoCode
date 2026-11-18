package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// A call owns one card from its first argument fragment through its result;
// repeated deltas and the later start event must not append duplicates.
func TestToolDeltaUpsertsOnePendingCard(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true

	m = drive(m, agentToolDeltaMsg{name: "file_write", args: `{"path":`, toolCallID: "c1"})
	m = drive(m, agentToolDeltaMsg{name: "file_write", args: `{"path":"a.txt"}`, toolCallID: "c1"})

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 card after 2 deltas, got %d", len(m.messages))
	}
	card := m.messages[0]
	if card.Role != RoleTool || card.Label != "🔧 file_write" {
		t.Errorf("card identity = %+v", card)
	}
	if card.Content != "running…\n"+`{"path":"a.txt"}` {
		t.Errorf("card preview = %q", card.Content)
	}

	m = drive(m, agentToolStartMsg{name: "file_write", args: `{"path":"a.txt"}`, toolCallID: "c1"})
	if len(m.messages) != 1 {
		t.Fatalf("start event duplicated the card: %d messages", len(m.messages))
	}

	m = drive(m, agentToolMsg{name: "file_write", result: "wrote", toolCallID: "c1"})
	if len(m.messages) != 1 {
		t.Fatalf("result duplicated the card: %d messages", len(m.messages))
	}
	if m.messages[0].Content != "wrote" {
		t.Errorf("result did not replace the card: %q", m.messages[0].Content)
	}
	if _, ok := m.pendingTools["c1"]; ok {
		t.Error("pending card mapping was not cleared by the result")
	}
}

// The delta preview keeps the same 200-character cap the start card had.
func TestToolDeltaPreviewTruncates(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	long := strings.Repeat("x", 500)
	m = drive(m, agentToolDeltaMsg{name: "file_write", args: long, toolCallID: "c1"})

	body := strings.TrimPrefix(m.messages[0].Content, "running…\n")
	if len(body) != 200 {
		t.Fatalf("preview length = %d, want 200", len(body))
	}
	if !strings.HasSuffix(body, "...") {
		t.Errorf("preview was not ellipsized: %q", body)
	}
}

// Enter while streaming queues steering instead of dropping it, and the chip
// clears when the engine's drain notice arrives.
func TestEnterWhileStreamingQueuesSteering(t *testing.T) {
	m := newTestModel(nil)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.streaming = true
	m.textarea.SetValue("focus on the parser")

	m = drive(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.queuedSteer != 1 {
		t.Fatalf("queuedSteer = %d, want 1", m.queuedSteer)
	}
	select {
	case got := <-m.steerCh:
		if got != "focus on the parser" {
			t.Errorf("steered text = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("steering was not forwarded to the steer channel")
	}
	if m.textarea.Value() != "" {
		t.Errorf("input was not cleared after queueing: %q", m.textarea.Value())
	}
	if chip := m.renderSteerChip(); !strings.Contains(chip, "1 steered message queued") {
		t.Errorf("chip = %q, want a queued count", chip)
	}

	m = drive(m, agentToolMsg{name: "GoCode", result: agent.SteeringNotice, steeringApplied: true})
	if m.queuedSteer != 0 {
		t.Errorf("queuedSteer = %d after the drain notice, want 0", m.queuedSteer)
	}
	if chip := m.renderSteerChip(); chip != "" {
		t.Errorf("chip still rendered after drain: %q", chip)
	}
}

// A turn ending clears the chip even if the last round never drained it.
func TestTurnDoneClearsSteeringChip(t *testing.T) {
	m := newTestModel(nil)
	m.queuedSteer = 2
	if chip := m.renderSteerChip(); !strings.Contains(chip, "2 steered messages queued") {
		t.Fatalf("chip = %q", chip)
	}
	m = drive(m, agentDoneMsg{})
	if m.queuedSteer != 0 || m.renderSteerChip() != "" {
		t.Errorf("chip survived turn end: %d %q", m.queuedSteer, m.renderSteerChip())
	}
}

// Steering notices are mapped to the tool-message path and flagged, so the
// model can clear the chip without parsing prose elsewhere.
func TestToTeaMsgMapsToolDeltaAndSteeringNotice(t *testing.T) {
	delta, ok := toTeaMsg(agent.LoopEvent{
		Kind: agent.EventToolDelta, Tool: "file_write", ToolCallID: "c1", Args: `{"a":`,
	}).(agentToolDeltaMsg)
	if !ok {
		t.Fatal("EventToolDelta did not map to agentToolDeltaMsg")
	}
	if delta.name != "file_write" || delta.toolCallID != "c1" || delta.args != `{"a":` {
		t.Errorf("delta message lost fields: %+v", delta)
	}

	notice, ok := toTeaMsg(agent.LoopEvent{Kind: agent.EventNotice, Text: agent.SteeringNotice}).(agentToolMsg)
	if !ok || !notice.steeringApplied {
		t.Fatalf("steering notice not flagged: %+v", notice)
	}
	plain, ok := toTeaMsg(agent.LoopEvent{Kind: agent.EventNotice, Text: "other"}).(agentToolMsg)
	if !ok || plain.steeringApplied {
		t.Errorf("unrelated notice flagged as steering: %+v", plain)
	}
}

// steerScriptProvider blocks its first round until released, so the test can
// push a message onto the steer channel while the turn is genuinely running.
type steerScriptProvider struct {
	started chan struct{}
	release chan struct{}
	round   int
}

func (p *steerScriptProvider) Name() string { return "steer-script" }

func (p *steerScriptProvider) Models(context.Context) ([]string, error) { return []string{"m"}, nil }

func (p *steerScriptProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.round++
	ch := make(chan provider.StreamChunk, 4)
	if p.round == 1 {
		close(p.started)
		<-p.release
		ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "missing_tool"}}}
		// Keep the round open briefly so the engine goroutine's steering pump
		// runs while the stream is still live, as it would on a real network.
		time.Sleep(50 * time.Millisecond)
	} else {
		ch <- provider.StreamChunk{Delta: "done"}
		ch <- provider.StreamChunk{Done: true}
	}
	close(ch)
	return ch, nil
}

// The engine goroutine forwards typed steering into the running turn, and the
// message lands at the next round boundary.
func TestEngineGoroutineAppliesSteeringAtRoundBoundary(t *testing.T) {
	p := &steerScriptProvider{started: make(chan struct{}), release: make(chan struct{})}
	sess := agent.NewSession("m")
	reg := provider.NewRegistry()
	reg.Register(p)
	engine := agent.NewAgentLoop(reg, sess, tools.NewRegistry(), tools.NewApprovalGate())

	inputCh := make(chan string, 1)
	steerCh := make(chan string, 8)
	outputCh := make(chan tea.Msg, 64)
	cancelCh := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runEngineGoroutine(ctx, engine, inputCh, steerCh, outputCh, cancelCh)

	inputCh <- "hello"
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not start")
	}

	steerCh <- "focus on tests"
	close(p.release)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case msg := <-outputCh:
			if done, ok := msg.(agentDoneMsg); ok {
				if done.err != nil {
					t.Fatalf("turn failed: %v", done.err)
				}
				goto finished
			}
		case <-deadline:
			t.Fatal("turn did not finish")
		}
	}

finished:
	steered := false
	for _, msg := range sess.History() {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, "[steering] ") {
			steered = true
			if !strings.Contains(msg.Content, "focus on tests") {
				t.Errorf("steering content = %q", msg.Content)
			}
		}
	}
	if !steered {
		t.Errorf("steering never reached the transcript: %+v", sess.History())
	}
}
