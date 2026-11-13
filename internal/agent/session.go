package agent

import (
	"bytes"
	"log/slog"
	"sync"

	"github.com/mevarx/GoCode/internal/provider"
	"github.com/mevarx/GoCode/internal/session"
)

type Session struct {
	id            string
	providerName  string
	model         string
	messages      []provider.Message
	store         *session.SessionStore
	modifiedFiles map[string]bool
	// scratchpad is agent-maintained working state that must survive history
	// compaction. It is rendered into the system prompt.
	scratchpad string
	mu         sync.RWMutex
}

func NewSession(model string) *Session {
	return &Session{
		model:         model,
		modifiedFiles: make(map[string]bool),
	}
}

func NewSessionWithStore(id, providerName, model string, store *session.SessionStore) *Session {
	return &Session{
		id:            id,
		providerName:  providerName,
		model:         model,
		store:         store,
		modifiedFiles: make(map[string]bool),
	}
}

func (s *Session) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *Session) SetID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *Session) Provider() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providerName
}

func (s *Session) SetProvider(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerName = p
	if s.store != nil && s.id != "" {
		if err := s.store.UpdateModel(s.id, s.providerName, s.model); err != nil {
			slog.Warn("failed to update provider in store", "error", err)
		}
	}
}

func (s *Session) Store() *session.SessionStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

// History safety net. AddMessage trims once a session exceeds
// maxHistoryMessages, keeping the system prompt and the most recent
// historyKeepTurns logical turns. Turns are never split, so a tool call keeps
// its results, and EnsureToolPairing re-establishes the wire invariant. A turn
// still being written (assistant calls without all results yet) is left alone;
// the request boundary settles it.
// POLICY: >500 messages -> keep the last 40 turns (was: last 200 messages).
const (
	maxHistoryMessages = 500
	historyKeepTurns   = 40
)

func (s *Session) AddMessage(msg provider.Message) {
	s.mu.Lock()
	s.messages = append(s.messages, msg)

	if len(s.messages) > maxHistoryMessages {
		s.messages = trimHistory(s.messages, historyKeepTurns)
	}

	st := s.store
	id := s.id
	s.mu.Unlock()

	if st != nil && id != "" {
		if err := st.AppendMessage(id, redactedForPersistence(msg)); err != nil {
			slog.Warn("failed to persist message", "session", id, "error", err)
		}
	}
}

// redactedForPersistence returns a copy of msg whose tool-call arguments have
// secrets masked. The in-memory message keeps the raw arguments so the tool
// still receives them during this turn; the transcript does not.
// SECURITY: persisted tool-call args are a transcript boundary like tool output.
func redactedForPersistence(msg provider.Message) provider.Message {
	if len(msg.ToolCalls) == 0 {
		return msg
	}
	calls := make([]provider.ToolCall, len(msg.ToolCalls))
	copy(calls, msg.ToolCalls)
	changed := false
	for i := range calls {
		redacted := session.RedactToolArgs(calls[i].Args)
		if !bytes.Equal(redacted, calls[i].Args) {
			calls[i].Args = redacted
			changed = true
		}
	}
	if !changed {
		return msg
	}
	msg.ToolCalls = calls
	return msg
}

func (s *Session) LoadMessages(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]provider.Message, len(msgs))
	copy(s.messages, msgs)
}

// SetSystemMessage makes content the session's only system message, replacing
// an existing prompt (and dropping duplicates) or prepending when none exists.
// Resumed transcripts carry the prompt of the workspace they were recorded in,
// so callers rebuild it instead of trusting the stored copy.
func (s *Session) SetSystemMessage(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	replaced := false
	kept := make([]provider.Message, 0, len(s.messages)+1)
	for _, m := range s.messages {
		if m.Role == "system" {
			if replaced {
				continue
			}
			replaced = true
			kept = append(kept, provider.Message{Role: "system", Content: content})
			continue
		}
		kept = append(kept, m)
	}
	if !replaced {
		kept = append([]provider.Message{{Role: "system", Content: content}}, kept...)
	}
	s.messages = kept
}

func (s *Session) History() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]provider.Message, len(s.messages))
	copy(out, s.messages)
	return out
}

func (s *Session) Model() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

// SetScratchpad replaces the agent-maintained working state and persists it
// when the session is bound to a store.
func (s *Session) SetScratchpad(content string) {
	s.mu.Lock()
	s.scratchpad = content
	st := s.store
	id := s.id
	s.mu.Unlock()

	if st != nil && id != "" {
		if err := st.UpdateScratchpad(id, content); err != nil {
			slog.Warn("failed to update scratchpad in store", "session", id, "error", err)
		}
	}
}

// Scratchpad returns the agent-maintained working state.
func (s *Session) Scratchpad() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scratchpad
}

func (s *Session) SetModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = model
	if s.store != nil && s.id != "" {
		if err := s.store.UpdateModel(s.id, s.providerName, s.model); err != nil {
			slog.Warn("failed to update model in store", "error", err)
		}
	}
}

func (s *Session) Clear() {
	s.mu.Lock()
	s.messages = nil
	st := s.store
	id := s.id
	p := s.providerName
	m := s.model
	s.mu.Unlock()

	if st != nil && id != "" {
		rec := &session.SessionRecord{
			ID:       id,
			Provider: p,
			Model:    m,
			Messages: nil,
		}
		if err := st.SaveSession(rec); err != nil {
			slog.Warn("failed to save cleared session", "session", id, "error", err)
		}
	}
}

func (s *Session) TrackModifiedFile(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modifiedFiles == nil {
		s.modifiedFiles = make(map[string]bool)
	}
	s.modifiedFiles[path] = true
}

func (s *Session) ModifiedFiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	files := make([]string, 0, len(s.modifiedFiles))
	for f := range s.modifiedFiles {
		files = append(files, f)
	}
	return files
}

func (s *Session) ClearModifiedFiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modifiedFiles = make(map[string]bool)
}

func (s *Session) LastAssistantMessage() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" {
			return s.messages[i].Content
		}
	}
	return ""
}
