package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
)

type mockProvider struct {
	name   string
	models []string
}

func (m *mockProvider) Name() string                                 { return m.name }
func (m *mockProvider) Models(ctx context.Context) ([]string, error) { return m.models, nil }
func (m *mockProvider) Stream(ctx context.Context, model string, history []provider.Message, tools []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	ch := make(chan provider.StreamChunk)
	close(ch)
	return ch, nil
}

func TestHandleCommand_Help(t *testing.T) {
	reg := provider.NewRegistry()
	sess := NewSession("m1")
	cmdCtx := CommandContext{
		Session:  sess,
		Registry: reg,
	}

	res := HandleCommand(context.Background(), cmdCtx, "/help")
	if !res.Handled {
		t.Fatal("expected /help to be handled")
	}
	if !strings.Contains(res.Output, "Available commands") {
		t.Errorf("expected help output, got: %s", res.Output)
	}
}

func TestHandleCommand_Exit(t *testing.T) {
	cmdCtx := CommandContext{}
	res := HandleCommand(context.Background(), cmdCtx, "exit")
	if !res.Handled || !res.Exit {
		t.Errorf("expected exit to be handled with Exit=true")
	}

	resQuit := HandleCommand(context.Background(), cmdCtx, "quit")
	if !resQuit.Handled || !resQuit.Exit {
		t.Errorf("expected quit to be handled with Exit=true")
	}
}

func TestHandleCommand_Clear(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "mock"})
	sess := NewSession("m1")
	sess.AddMessage(provider.Message{Role: "user", Content: "hello"})

	cmdCtx := CommandContext{
		Session:  sess,
		Registry: reg,
	}

	res := HandleCommand(context.Background(), cmdCtx, "/clear")
	if !res.Handled {
		t.Fatal("expected /clear to be handled")
	}

	// Should have cleared user message and re-added system message
	history := sess.History()
	if len(history) != 1 || history[0].Role != "system" {
		t.Errorf("expected only system message after clear, got %d messages", len(history))
	}
}

func TestHandleCommand_StatsAndCompact(t *testing.T) {
	sess := NewSession("m1")
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 1"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 1"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 2"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 2"})
	sess.AddMessage(provider.Message{Role: "user", Content: "turn 3"})
	sess.AddMessage(provider.Message{Role: "assistant", Content: "resp 3"})

	cmdCtx := CommandContext{
		Session:        sess,
		ContextManager: NewContextManager(8192),
	}

	resStats := HandleCommand(context.Background(), cmdCtx, "/stats")
	if !resStats.Handled || !strings.Contains(resStats.Output, "tokens") {
		t.Errorf("expected stats output with tokens, got: %s", resStats.Output)
	}

	resCompact := HandleCommand(context.Background(), cmdCtx, "/compact")
	if !resCompact.Handled || !strings.Contains(resCompact.Output, "Compacted history") {
		t.Errorf("expected compact output, got: %s", resCompact.Output)
	}
}

func TestHandleCommand_ProvidersAndModels(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "provA", models: []string{"model-1", "model-2"}})
	reg.Register(&mockProvider{name: "provB", models: []string{"model-3"}})
	sess := NewSession("model-1")

	cmdCtx := CommandContext{
		Session:  sess,
		Registry: reg,
	}

	// List providers
	resP := HandleCommand(context.Background(), cmdCtx, "/providers")
	if !resP.Handled || !strings.Contains(resP.Output, "provA") || !strings.Contains(resP.Output, "provB") {
		t.Errorf("expected providers listed, got: %s", resP.Output)
	}

	// Switch provider
	resSwitch := HandleCommand(context.Background(), cmdCtx, "/provider provB")
	if !resSwitch.Handled || !strings.Contains(resSwitch.Output, "Switched provider to provB") {
		t.Errorf("expected provider switched, got: %s", resSwitch.Output)
	}
	if reg.ActiveName() != "provB" {
		t.Errorf("expected active provider provB, got %s", reg.ActiveName())
	}

	// Show model
	resM := HandleCommand(context.Background(), cmdCtx, "/model")
	if !resM.Handled || !strings.Contains(resM.Output, "Active Model") {
		t.Errorf("expected active model output, got: %s", resM.Output)
	}

	// Set model
	resSetM := HandleCommand(context.Background(), cmdCtx, "/model model-3")
	if !resSetM.Handled || sess.Model() != "model-3" {
		t.Errorf("expected model set to model-3, got %s", sess.Model())
	}
}

func TestHandleCommand_Commit_NoModifiedFiles(t *testing.T) {
	sess := NewSession("m1")
	cmdCtx := CommandContext{
		Session: sess,
	}

	res := HandleCommand(context.Background(), cmdCtx, "/commit Initial commit")
	if !res.Handled || !strings.Contains(res.Output, "No modified files") {
		t.Errorf("expected 'No modified files', got: %s", res.Output)
	}
}

func TestHandleCommand_Commit_CancelledByUser(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession("m1")
	filePath := filepath.Join(dir, "changed.txt")
	os.WriteFile(filePath, []byte("change"), 0o644)
	sess.TrackModifiedFile(filePath)

	cmdCtx := CommandContext{
		Session:       sess,
		WorkspaceRoot: dir,
		AskApproval: func(prompt string) bool {
			return false // User denies
		},
	}

	res := HandleCommand(context.Background(), cmdCtx, "/commit Add changed.txt")
	if !res.Handled || !strings.Contains(res.Output, "cancelled by user") {
		t.Errorf("expected commit cancelled by user, got: %s", res.Output)
	}
}
