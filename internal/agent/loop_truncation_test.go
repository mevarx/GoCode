package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

// scriptedProvider replays a fixed chunk sequence, letting a test shape exactly
// how a turn ends (truncation, tool calls, empty answer).
type scriptedProvider struct {
	chunks []provider.StreamChunk
}

func (s *scriptedProvider) Name() string { return "scripted" }

func (s *scriptedProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (s *scriptedProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk, len(s.chunks))
	for _, c := range s.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

// turnEvents runs one turn and returns every event the loop reported.
func turnEvents(t *testing.T, p provider.Provider) ([]LoopEvent, *Session, error) {
	t.Helper()

	sess := NewSession("m")
	engine := engineWith(p, sess)
	var events []LoopEvent
	engine.Observe = func(ev LoopEvent) { events = append(events, ev) }
	err := engine.RunTurn(context.Background(), "go")
	return events, sess, err
}

func countNotices(events []LoopEvent, substr string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == EventNotice && strings.Contains(ev.Text, substr) {
			n++
		}
	}
	return n
}

// Regression: the parser used to report truncation as a chunk error, so the
// loop returned before AddMessage and the text already streamed vanished.
func TestRunTurnKeepsPartialAnswerOnTruncation(t *testing.T) {
	p := &scriptedProvider{chunks: []provider.StreamChunk{
		{Delta: "partial answer"},
		{Done: true, FinishReason: "length"},
	}}

	events, sess, err := turnEvents(t, p)
	if err != nil {
		t.Fatalf("truncation must not fail the turn, got %v", err)
	}

	var assistant string
	var found bool
	for _, msg := range sess.History() {
		if msg.Role == "assistant" {
			assistant = msg.Content
			found = true
		}
	}
	if !found {
		t.Fatal("truncated answer was not persisted to history")
	}
	if assistant != "partial answer" {
		t.Errorf("expected persisted assistant content %q, got %q", "partial answer", assistant)
	}

	if n := countNotices(events, "token limit"); n != 1 {
		t.Errorf("expected exactly one token-limit notice, got %d", n)
	}
}

// A turn truncated before any text still ends cleanly, and only the token-limit
// notice explains it (no misleading credentials hint).
func TestRunTurnTruncationWithoutDeltasEndsGracefully(t *testing.T) {
	p := &scriptedProvider{chunks: []provider.StreamChunk{
		{Done: true, FinishReason: "max_tokens"},
	}}

	events, _, err := turnEvents(t, p)
	if err != nil {
		t.Fatalf("empty truncated turn must not fail, got %v", err)
	}

	if n := countNotices(events, "token limit"); n != 1 {
		t.Errorf("expected exactly one token-limit notice, got %d", n)
	}
	if n := countNotices(events, "No response received"); n != 0 {
		t.Errorf("truncation must not be reported as a credential problem, got %d notices", n)
	}
}
