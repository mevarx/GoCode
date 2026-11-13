package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/tools"
)

// A session resumed from another workspace must not send its stale prompt:
// EnsureSystemPrompt rebuilds it from the current run's workspace.
func TestEnsureSystemPromptRebuildsResumedPrompt(t *testing.T) {
	workspaceB := filepath.Join(t.TempDir(), "workspace-b")
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatalf("failed to create workspace B: %v", err)
	}

	sess := NewSession("gpt-4o")
	// Simulates a transcript loaded from repo A's persisted session.
	sess.AddMessage(provider.Message{Role: "system", Content: "OLD_WORKSPACE_A_MARKER"})
	sess.AddMessage(provider.Message{Role: "user", Content: "hello"})

	loop := NewAgentLoop(provider.NewRegistry(), sess, tools.NewRegistry(), tools.NewApprovalGate())
	loop.WorkspaceRoot = workspaceB

	loop.EnsureSystemPrompt()

	history := sess.History()
	systemCount := 0
	for _, m := range history {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Fatalf("expected exactly one system message, got %d", systemCount)
	}
	if history[0].Role != "system" {
		t.Fatalf("expected the system message first, got %q", history[0].Role)
	}
	if !strings.Contains(history[0].Content, "workspace-b") {
		t.Errorf("rebuilt prompt does not describe workspace B: %q", history[0].Content)
	}
	if strings.Contains(history[0].Content, "OLD_WORKSPACE_A_MARKER") {
		t.Errorf("stale prompt from workspace A survived: %q", history[0].Content)
	}
	if len(history) != 2 || history[1].Content != "hello" {
		t.Errorf("conversation history must be preserved: %+v", history)
	}
}

func TestSetSystemMessagePrependsWhenAbsent(t *testing.T) {
	sess := NewSession("m")
	sess.AddMessage(provider.Message{Role: "user", Content: "hello"})

	sess.SetSystemMessage("prompt")

	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	if history[0].Role != "system" || history[0].Content != "prompt" {
		t.Fatalf("expected prompt prepended, got %+v", history[0])
	}
}

// Duplicate system messages from an older transcript collapse to one.
func TestSetSystemMessageRemovesDuplicates(t *testing.T) {
	sess := NewSession("m")
	sess.LoadMessages([]provider.Message{
		{Role: "system", Content: "first"},
		{Role: "system", Content: "second"},
		{Role: "user", Content: "hello"},
	})

	sess.SetSystemMessage("fresh")

	history := sess.History()
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(history), history)
	}
	if history[0].Role != "system" || history[0].Content != "fresh" {
		t.Errorf("unexpected system message: %+v", history[0])
	}
	if history[1].Role != "user" {
		t.Errorf("expected user message preserved, got %+v", history[1])
	}
}
