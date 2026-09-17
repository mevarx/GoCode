package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mevarx/GoCode/internal/agent"
	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

type blockingProvider struct{}

func (b *blockingProvider) Name() string { return "blocking" }

func (b *blockingProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (b *blockingProvider) Stream(ctx context.Context, model string, history []provider.Message, toolSpecs []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func TestInterruptCancelsRunningTurn(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&blockingProvider{})
	sess := agent.NewSession("m")
	approval := tools.NewApprovalGate()
	inputCh := make(chan string, 1)
	outputCh := make(chan tea.Msg, 64)
	cancelCh := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runAgentGoroutine(ctx, r, sess, tools.NewRegistry(), approval, inputCh, outputCh, cancelCh, "")
		close(done)
	}()

	inputCh <- "hello"
	cancelCh <- struct{}{}

	select {
	case msg := <-outputCh:
		doneMsg, ok := msg.(agentDoneMsg)
		if !ok {
			t.Fatalf("expected agentDoneMsg, got %T", msg)
		}
		if !errors.Is(doneMsg.err, context.Canceled) {
			t.Fatalf("expected context.Canceled after interrupt, got %v", doneMsg.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not complete after interrupt")
	}

	// The turn must be over and ready for the next input.
	select {
	case <-done:
		t.Fatal("agent goroutine exited unexpectedly")
	default:
	}
}

type fakeProvider struct {
	name        string
	models      []string
	modelsError error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Models(context.Context) ([]string, error) {
	return f.models, f.modelsError
}

func (f *fakeProvider) Stream(context.Context, string, []provider.Message, []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("stream not supported in tests")
}

func newTestRegistry(models []string) *provider.Registry {
	r := provider.NewRegistry()
	r.Register(&fakeProvider{name: "ollama", models: models})
	return r
}

func drainOneToolMsg(t *testing.T, out <-chan tea.Msg) agentToolMsg {
	t.Helper()
	select {
	case msg := <-out:
		tm, ok := msg.(agentToolMsg)
		if !ok {
			t.Fatalf("expected agentToolMsg, got %T", msg)
		}
		return tm
	default:
		t.Fatal("no message produced")
		return agentToolMsg{}
	}
}

func TestUnknownSlashCommandIsRejected(t *testing.T) {
	reg := newTestRegistry([]string{"llama3"})
	sess := agent.NewSession("llama3")
	out := make(chan tea.Msg, 8)

	if !handleSlashCommand(context.Background(), "/bogus", reg, sess, out, "") {
		t.Fatal("expected unknown slash command to be handled")
	}
	tm := drainOneToolMsg(t, out)
	if !tm.isError {
		t.Fatal("expected error message for unknown command")
	}
	if !strings.Contains(tm.result, "/help") {
		t.Errorf("expected result to mention /help, got %q", tm.result)
	}
}

func TestPlainMessageIsNotACommand(t *testing.T) {
	reg := newTestRegistry([]string{"llama3"})
	sess := agent.NewSession("llama3")
	out := make(chan tea.Msg, 8)

	if handleSlashCommand(context.Background(), "hello there", reg, sess, out, "") {
		t.Fatal("plain message must not be handled as a slash command")
	}
}

func TestModelSwitchValidatesAgainstProvider(t *testing.T) {
	reg := newTestRegistry([]string{"gpt-4o", "gemini-2.5-flash"})
	sess := agent.NewSession("gpt-4o")
	out := make(chan tea.Msg, 8)

	if !handleSlashCommand(context.Background(), "/model bogus", reg, sess, out, "") {
		t.Fatal("expected /model to be handled")
	}
	tm := drainOneToolMsg(t, out)
	if !tm.isError {
		t.Fatalf("unknown model should be rejected, got %q", tm.result)
	}
	if sess.Model() != "gpt-4o" {
		t.Errorf("model should stay gpt-4o, got %q", sess.Model())
	}

	if !handleSlashCommand(context.Background(), "/model gemini-2.5-flash", reg, sess, out, "") {
		t.Fatal("expected /model to be handled")
	}
	tm = drainOneToolMsg(t, out)
	if tm.isError {
		t.Fatalf("valid model should be accepted, got %q", tm.result)
	}
	if sess.Model() != "gemini-2.5-flash" {
		t.Errorf("expected model gemini-2.5-flash, got %q", sess.Model())
	}
}

func TestModelSwitchSkipsValidationWhenListUnavailable(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&fakeProvider{name: "ollama", modelsError: errors.New("offline")})
	sess := agent.NewSession("llama3")
	out := make(chan tea.Msg, 8)

	if !handleSlashCommand(context.Background(), "/model llama3.1", r, sess, out, "") {
		t.Fatal("expected /model to be handled")
	}
	tm := drainOneToolMsg(t, out)
	if tm.isError {
		t.Fatalf("validation must be skipped when model list fails, got %q", tm.result)
	}
	if sess.Model() != "llama3.1" {
		t.Errorf("expected model llama3.1, got %q", sess.Model())
	}
}

func TestModelQueryShowsActive(t *testing.T) {
	reg := newTestRegistry([]string{"gpt-4o", "gemini-2.5-flash"})
	sess := agent.NewSession("gpt-4o")
	out := make(chan tea.Msg, 8)

	if !handleSlashCommand(context.Background(), "/model", reg, sess, out, "") {
		t.Fatal("expected /model to be handled")
	}
	tm := drainOneToolMsg(t, out)
	if tm.isError {
		t.Fatalf("expected model info, got %q", tm.result)
	}
	if !strings.Contains(tm.result, "gpt-4o") {
		t.Errorf("expected active model in result, got %q", tm.result)
	}
}
