package agent

import (
	"github.com/mevarx/GoCode/internal/provider"
)

type Session struct {
	messages []provider.Message
	model    string
}

func NewSession(model string) *Session {
	return &Session{
		model: model,
	}
}

func (s *Session) AddMessage(msg provider.Message) {
	s.messages = append(s.messages, msg)

	if len(s.messages) > 500 {
		var trimmed []provider.Message
		if s.messages[0].Role == "system" {
			trimmed = append(trimmed, s.messages[0])
			trimmed = append(trimmed, s.messages[len(s.messages)-200:]...)
		} else {
			trimmed = make([]provider.Message, 200)
			copy(trimmed, s.messages[len(s.messages)-200:])
		}
		s.messages = trimmed
	}
}

func (s *Session) History() []provider.Message {
	return s.messages
}

func (s *Session) Model() string {
	return s.model
}

func (s *Session) SetModel(model string) {
	s.model = model
}

func (s *Session) Clear() {
	s.messages = nil
}

func (s *Session) LastAssistantMessage() string {
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].Role == "assistant" {
			return s.messages[i].Content
		}
	}
	return ""
}
