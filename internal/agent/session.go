package agent

import (
	"github.com/mevarx/GoCode/internal/provider"
)

// Session tracks conversation state and message history.
type Session struct {
	messages []provider.Message
	model    string
}

// NewSession initializes a new session for the given model.
func NewSession(model string) *Session {
	return &Session{
		model: model,
	}
}

// AddMessage appends a message to the session history.
func (s *Session) AddMessage(msg provider.Message) {
	s.messages = append(s.messages, msg)
}

// History returns the full message history.
func (s *Session) History() []provider.Message {
	return s.messages
}

// Model returns the current model name.
func (s *Session) Model() string {
	return s.model
}

// SetModel updates the current model name.
func (s *Session) SetModel(model string) {
	s.model = model
}

// Clear resets message history.
func (s *Session) Clear() {
	s.messages = nil
}

// LastAssistantMessage returns the latest assistant response content.
func (s *Session) LastAssistantMessage() string {
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" {
			return s.messages[i].Content
		}
	}
	return ""
}
