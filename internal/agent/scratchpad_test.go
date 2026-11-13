package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
	"github.com/mevarx/GoCode/internal/tools"
)

func TestBuildSystemPrompt_TaskScratchpadPlacement(t *testing.T) {
	prompt := BuildSystemPromptWithOptions(PromptOptions{
		Plan:           "- [ ] (1) Implement C1",
		Scratchpad:     "Goal: finish context compaction\nOpen: internal/agent/context.go",
		ProjectContext: "Project guidelines",
		GlobalContext:  "User preference",
	})

	planIdx := strings.Index(prompt, "## Current Plan")
	scratchIdx := strings.Index(prompt, "## Task Scratchpad")
	projectIdx := strings.Index(prompt, projectContextBegin)
	if planIdx < 0 || scratchIdx < 0 || projectIdx < 0 {
		t.Fatalf("missing expected sections:\n%s", prompt)
	}
	if !(planIdx < scratchIdx && scratchIdx < projectIdx) {
		t.Fatalf("scratchpad must follow the plan and precede untrusted context (plan=%d scratch=%d project=%d)", planIdx, scratchIdx, projectIdx)
	}
	if !strings.Contains(prompt, "agent-maintained state, not policy") {
		t.Fatalf("scratchpad must be labelled as state, not policy:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Open: internal/agent/context.go") {
		t.Fatalf("scratchpad content missing:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_EmptyScratchpadOmitted(t *testing.T) {
	prompt := BuildSystemPromptWithOptions(PromptOptions{Scratchpad: "   \n  "})
	if strings.Contains(prompt, "## Task Scratchpad") {
		t.Fatalf("empty scratchpad must not render a section:\n%s", prompt)
	}
}

// scratchpadCallProvider calls the scratchpad tool on its first round and
// finishes on the second, letting one RunTurn exercise the store→session sync.
type scratchpadCallProvider struct {
	mu        sync.Mutex
	calls     int
	histories [][]provider.Message
}

func (p *scratchpadCallProvider) Name() string { return "scratchpad-fake" }

func (p *scratchpadCallProvider) Models(context.Context) ([]string, error) {
	return []string{"m"}, nil
}

func (p *scratchpadCallProvider) Stream(_ context.Context, _ string, history []provider.Message, _ []provider.ToolSpec) (<-chan provider.StreamChunk, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.histories = append(p.histories, history)
	p.mu.Unlock()

	ch := make(chan provider.StreamChunk, 2)
	if call == 1 {
		ch <- provider.StreamChunk{ToolCalls: []provider.ToolCall{{
			ID:   "sp1",
			Name: "scratchpad",
			Args: json.RawMessage(`{"content":"Goal: ship C1\nDecision: use a store"}`),
		}}}
	} else {
		ch <- provider.StreamChunk{Delta: "done"}
	}
	ch <- provider.StreamChunk{Done: true}
	close(ch)
	return ch, nil
}

func TestScratchpadToolSyncsSessionAndPrompt(t *testing.T) {
	sess := NewSession("m")
	store := tools.NewScratchpadStore()

	toolReg := tools.NewRegistry()
	if err := toolReg.Register(&tools.ScratchpadTool{Store: store}); err != nil {
		t.Fatalf("register scratchpad tool: %v", err)
	}

	reg := provider.NewRegistry()
	reg.Register(&scratchpadCallProvider{})

	engine := NewAgentLoop(reg, sess, toolReg, tools.NewApprovalGate())
	engine.Scratchpad = store

	if err := engine.RunTurn(context.Background(), "go"); err != nil {
		t.Fatalf("RunTurn returned error: %v", err)
	}

	if got := sess.Scratchpad(); !strings.Contains(got, "Goal: ship C1") {
		t.Fatalf("scratchpad was not mirrored into the session: %q", got)
	}
	history := sess.History()
	if len(history) == 0 || history[0].Role != "system" {
		t.Fatalf("expected a system prompt, got %+v", history)
	}
	if !strings.Contains(history[0].Content, "## Task Scratchpad") {
		t.Fatalf("system prompt was not rebuilt with the scratchpad:\n%s", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "Goal: ship C1") {
		t.Fatalf("scratchpad content missing from prompt:\n%s", history[0].Content)
	}
}

func TestScratchpadSurvivesCompactWithSummary(t *testing.T) {
	sess := NewSession("m")
	sess.SetScratchpad("Goal: survive compaction\nOpen: context.go")

	reg := provider.NewRegistry()
	reg.Register(&summaryProvider{summary: []string{"condensed earlier work"}})

	engine := NewAgentLoop(reg, sess, tools.NewRegistry(), tools.NewApprovalGate())
	engine.EnsureSystemPrompt()

	for i := range 3 {
		sess.AddMessage(provider.Message{Role: "user", Content: "question " + string(rune('1'+i))})
		sess.AddMessage(provider.Message{Role: "assistant", Content: "answer " + string(rune('1'+i))})
	}

	compacted, err := engine.ContextManager.CompactWithSummary(context.Background(), reg.Get("summary-fake"), "m", sess.History(), 1)
	if err != nil {
		t.Fatalf("CompactWithSummary returned error: %v", err)
	}
	sess.LoadMessages(compacted)
	engine.EnsureSystemPrompt()

	history := sess.History()
	if len(history) == 0 || history[0].Role != "system" {
		t.Fatalf("expected system prompt after compaction, got %+v", history)
	}
	if !strings.Contains(history[0].Content, "Goal: survive compaction") {
		t.Fatalf("scratchpad was lost across compaction:\n%s", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "## Task Scratchpad") {
		t.Fatalf("scratchpad section missing from rebuilt prompt:\n%s", history[0].Content)
	}
}

func TestSessionSetScratchpadPersists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	rec, err := store.CreateSessionWithWorkspace("s1", "title", "p", "m", "")
	if err != nil {
		t.Fatalf("CreateSessionWithWorkspace: %v", err)
	}

	sess := NewSessionWithStore(rec.ID, rec.Provider, rec.Model, store)
	sess.SetScratchpad("Goal: persist across restarts")

	stored, err := store.GetSession(rec.ID)
	if err != nil || stored == nil {
		t.Fatalf("GetSession: rec=%v err=%v", stored, err)
	}
	if stored.Scratchpad != "Goal: persist across restarts" {
		t.Fatalf("session scratchpad not persisted: %q", stored.Scratchpad)
	}
}

func TestHandleCommand_NewClearsScratchpad(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	rec, err := store.CreateSessionWithWorkspace("s1", "title", "mock", "m", "")
	if err != nil {
		t.Fatalf("CreateSessionWithWorkspace: %v", err)
	}

	sess := NewSessionWithStore(rec.ID, rec.Provider, rec.Model, store)
	sess.SetScratchpad("old working state")

	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "mock"})

	res := HandleCommand(context.Background(), CommandContext{Session: sess, Registry: reg}, "/new")
	if !res.Handled || res.Error != nil {
		t.Fatalf("expected handled success, got %+v", res)
	}
	if got := sess.Scratchpad(); got != "" {
		t.Fatalf("new session kept stale scratchpad: %q", got)
	}
}
