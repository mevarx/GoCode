package agent

import (
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
	mu            sync.RWMutex
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

func (s *Session) SetStore(st *session.SessionStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = st
}

func (s *Session) AddMessage(msg provider.Message) {
	s.mu.Lock()
	s.messages = append(s.messages, msg)

	if len(s.messages) > 500 {
		s.messages = trimMessages(s.messages, 200)
	}

	st := s.store
	id := s.id
	s.mu.Unlock()

	if st != nil && id != "" {
		if err := st.AppendMessage(id, msg); err != nil {
			slog.Warn("failed to persist message", "session", id, "error", err)
		}
	}
}

func trimMessages(messages []provider.Message, keep int) []provider.Message {
	var systemMsg *provider.Message
	start := 0
	if len(messages) > 0 && messages[0].Role == "system" {
		sys := messages[0]
		systemMsg = &sys
		start = 1
	}

	rest := messages[start:]
	cutIdx := len(rest) - keep
	if cutIdx < 0 {
		cutIdx = 0
	}

	for cutIdx < len(rest) && rest[cutIdx].Role == "tool" {
		cutIdx++
	}

	trimmed := make([]provider.Message, 0, len(rest)-cutIdx+1)
	if systemMsg != nil {
		trimmed = append(trimmed, *systemMsg)
	}
	trimmed = append(trimmed, rest[cutIdx:]...)
	return trimmed
}

func (s *Session) LoadMessages(msgs []provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]provider.Message, len(msgs))
	copy(s.messages, msgs)
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

