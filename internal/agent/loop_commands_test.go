package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

type cmdFakeProvider struct {
	models      []string
	modelsError error
}

func (f *cmdFakeProvider) Name() string { return "fake" }

func (f *cmdFakeProvider) Models(context.Context) ([]string, error) {
	return f.models, f.modelsError
}

func (f *cmdFakeProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("stream not supported in tests")
}

func cmdEngine(models []string, modelsErr error, sess *Session) (*AgentLoop, *[]LoopEvent) {
	reg := provider.NewRegistry()
	reg.Register(&cmdFakeProvider{models: models, modelsError: modelsErr})
	loop := NewAgentLoop(reg, sess, tools.NewRegistry(), tools.NewApprovalGate())
	events := &[]LoopEvent{}
	loop.Observe = func(ev LoopEvent) { *events = append(*events, ev) }
	return loop, events
}

func lastNotice(t *testing.T, events []LoopEvent) (LoopEvent, bool) {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == EventNotice || events[i].Kind == EventError || events[i].Kind == EventExit {
			return events[i], true
		}
	}
	return LoopEvent{}, false
}

func TestUnknownSlashCommandIsRejected(t *testing.T) {
	sess := NewSession("llama3")
	loop, events := cmdEngine([]string{"llama3"}, nil, sess)

	if err := loop.RunTurn(context.Background(), "/bogus"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev, ok := lastNotice(t, *events)
	if !ok {
		t.Fatal("unknown slash command produced no event")
	}
	if ev.Kind != EventError {
		t.Errorf("expected an error event, got kind %v", ev.Kind)
	}
	if !strings.Contains(ev.Text, "/help") {
		t.Errorf("expected result to mention /help, got %q", ev.Text)
	}
}

func TestModelSwitchValidatesAgainstProvider(t *testing.T) {
	sess := NewSession("gpt-4o")
	loop, events := cmdEngine([]string{"gpt-4o", "gemini-2.5-flash"}, nil, sess)

	_ = loop.RunTurn(context.Background(), "/model bogus")
	ev, _ := lastNotice(t, *events)
	if ev.Kind != EventError {
		t.Fatalf("unknown model should be rejected, got kind %v (%q)", ev.Kind, ev.Text)
	}
	if sess.Model() != "gpt-4o" {
		t.Errorf("model should stay gpt-4o, got %q", sess.Model())
	}

	*events = nil
	_ = loop.RunTurn(context.Background(), "/model gemini-2.5-flash")
	ev, _ = lastNotice(t, *events)
	if ev.Kind == EventError {
		t.Fatalf("valid model should be accepted, got %q", ev.Text)
	}
	if sess.Model() != "gemini-2.5-flash" {
		t.Errorf("expected model gemini-2.5-flash, got %q", sess.Model())
	}
}

func TestModelSwitchSkipsValidationWhenListUnavailable(t *testing.T) {
	sess := NewSession("llama3")
	loop, events := cmdEngine(nil, errors.New("offline"), sess)

	_ = loop.RunTurn(context.Background(), "/model llama3.1")
	ev, _ := lastNotice(t, *events)
	if ev.Kind == EventError {
		t.Fatalf("validation must be skipped when model list fails, got %q", ev.Text)
	}
	if sess.Model() != "llama3.1" {
		t.Errorf("expected model llama3.1, got %q", sess.Model())
	}
}

func TestModelQueryShowsActive(t *testing.T) {
	sess := NewSession("gpt-4o")
	loop, events := cmdEngine([]string{"gpt-4o", "gemini-2.5-flash"}, nil, sess)

	_ = loop.RunTurn(context.Background(), "/model")
	ev, _ := lastNotice(t, *events)
	if ev.Kind == EventError {
		t.Fatalf("expected model info, got %q", ev.Text)
	}
	if !strings.Contains(ev.Text, "gpt-4o") {
		t.Errorf("expected active model in result, got %q", ev.Text)
	}
}

func TestExitAndQuitProduceExitEvent(t *testing.T) {
	for _, cmd := range []string{"/exit", "/quit"} {
		sess := NewSession("llama3")
		loop, events := cmdEngine([]string{"llama3"}, nil, sess)

		err := loop.RunTurn(context.Background(), cmd)
		if !IsExitRequest(err) {
			t.Errorf("%s: expected exit request, got %v", cmd, err)
		}
		var sawExit bool
		for _, ev := range *events {
			if ev.Kind == EventExit {
				sawExit = true
			}
		}
		if !sawExit {
			t.Errorf("%s: expected an EventExit", cmd)
		}
	}
}

func TestEmptyInputIsANoOp(t *testing.T) {
	sess := NewSession("m")
	loop, events := cmdEngine([]string{"m"}, nil, sess)

	if err := loop.RunTurn(context.Background(), "   "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*events) != 0 {
		t.Errorf("empty input produced %d events, want 0", len(*events))
	}
	if len(sess.History()) != 0 {
		t.Errorf("empty input should not touch the session, got %d messages", len(sess.History()))
	}
}
