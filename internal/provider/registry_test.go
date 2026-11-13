package provider

import "testing"

func TestContextWindowDefaults(t *testing.T) {
	r := NewRegistry()

	cases := []struct {
		model string
		want  int
	}{
		{"gpt-4o-mini", 128000},
		{"gpt-4.1-2025-04-14", 128000},
		{"o1-preview", 200000},
		{"o3-mini", 200000},
		{"claude-sonnet-4-20250514", 200000},
		{"CLAUDE-OPUS-4", 200000},
		{"gemini-2.5-pro", 1000000},
		{"deepseek-chat", 128000},
		{"qwen-max", 128000},
		{"kimi-k2", 128000},
		{"llama-3.3-70b", 131072},
		{"some-unknown-model", defaultContextWindow},
		{"", defaultContextWindow},
	}

	for _, tc := range cases {
		if got := r.ContextWindow(tc.model); got != tc.want {
			t.Errorf("ContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestContextWindowLongestPrefixWins(t *testing.T) {
	r := NewRegistry()
	r.RegisterContextWindow("gpt-4o-mini", 64000)
	r.RegisterContextWindow("Gpt-4o-mini-2024", 32000)

	if got := r.ContextWindow("gpt-4o-mini-2024-07-18"); got != 32000 {
		t.Errorf("longest registered prefix should win, got %d", got)
	}
	if got := r.ContextWindow("gpt-4o-mini"); got != 64000 {
		t.Errorf("model-specific registration should beat the family default, got %d", got)
	}
	if got := r.ContextWindow("gpt-4o"); got != 128000 {
		t.Errorf("unrelated model must keep the family default, got %d", got)
	}
}

func TestRegisterContextWindowRejectsInvalid(t *testing.T) {
	r := NewRegistry()
	r.RegisterContextWindow("", 1000)
	r.RegisterContextWindow("m", 0)
	r.RegisterContextWindow("m", -1)

	if got := r.ContextWindow("m"); got != defaultContextWindow {
		t.Errorf("invalid registrations must be ignored, got %d", got)
	}
}
