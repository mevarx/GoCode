package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// MaxScratchpadBytes bounds the working-state text the model may record. It is
// small on purpose: the scratchpad is a compaction-safe summary, not a log.
const MaxScratchpadBytes = 8 * 1024

// ScratchpadStore holds agent-maintained working state (goal, decisions, open
// files) that must survive history compaction. It is shared by the scratchpad
// tool, which writes it, and the agent loop, which mirrors it into the session
// and therefore into the system prompt.
type ScratchpadStore struct {
	mu      sync.RWMutex
	content string
}

// NewScratchpadStore returns an empty store.
func NewScratchpadStore() *ScratchpadStore {
	return &ScratchpadStore{}
}

// Set replaces the whole scratchpad. Content larger than MaxScratchpadBytes is
// rejected and leaves the previous value untouched. An empty value clears it.
func (s *ScratchpadStore) Set(content string) error {
	if len(content) > MaxScratchpadBytes {
		return fmt.Errorf("scratchpad content is %d bytes; limit is %d", len(content), MaxScratchpadBytes)
	}
	s.mu.Lock()
	s.content = content
	s.mu.Unlock()
	return nil
}

// Append adds content after the existing scratchpad, separating the parts with
// a newline. The combined text must fit MaxScratchpadBytes.
func (s *ScratchpadStore) Append(content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := content
	if strings.TrimSpace(s.content) != "" && content != "" {
		next = s.content + "\n" + content
	} else if s.content != "" {
		next = s.content
	}
	if len(next) > MaxScratchpadBytes {
		return fmt.Errorf("scratchpad content would be %d bytes; limit is %d", len(next), MaxScratchpadBytes)
	}
	s.content = next
	return nil
}

// Get returns the raw scratchpad text.
func (s *ScratchpadStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.content
}

// Render returns the scratchpad text trimmed for prompt rendering. An empty
// scratchpad renders as the empty string.
func (s *ScratchpadStore) Render() string {
	return strings.TrimSpace(s.Get())
}

// ScratchpadTool lets the model record working state that must survive
// compaction. It only records text; it cannot touch the workspace, so it needs
// no approval.
type ScratchpadTool struct {
	Store *ScratchpadStore
}

type scratchpadArgs struct {
	Content *string `json:"content"`
	Mode    string  `json:"mode"`
}

func (t *ScratchpadTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "scratchpad",
		Description: "Record working state that must survive context compaction: the current goal, decisions already made, open files, and next steps. Use replace to rewrite the whole scratchpad or append to add to it. Keep it short and current; it is rendered into the system prompt.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["content"],
			"properties": {
				"content": {
					"type": "string",
					"description": "Working-state text to store (maximum 8192 bytes)"
				},
				"mode": {
					"type": "string",
					"enum": ["replace", "append"],
					"description": "replace overwrites the scratchpad; append adds to the existing text (default replace)"
				}
			}
		}`),
	}
}

// RequiresApproval is false: the tool only records text in the session.
func (t *ScratchpadTool) RequiresApproval() bool {
	return false
}

func (t *ScratchpadTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	if t.Store == nil {
		return Result{Error: "scratchpad store is not configured"}, nil
	}

	var a scratchpadArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{Error: fmt.Sprintf("invalid arguments: %v", err)}, nil
	}
	if a.Content == nil {
		return Result{Error: "content is required"}, nil
	}
	content := *a.Content

	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" {
		mode = "replace"
	}

	var err error
	switch mode {
	case "replace":
		err = t.Store.Set(content)
	case "append":
		err = t.Store.Append(content)
	default:
		return Result{Error: fmt.Sprintf("unknown mode %q (expected replace or append)", a.Mode)}, nil
	}
	if err != nil {
		return Result{Error: err.Error()}, nil
	}

	return Result{Output: fmt.Sprintf("Scratchpad updated (%s, %d bytes).", mode, len(t.Store.Get()))}, nil
}
