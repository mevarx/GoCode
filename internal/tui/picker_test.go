package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
)

func TestModelItem(t *testing.T) {
	item := ModelItem{
		Provider: "openai",
		Model:    "gpt-4o",
	}

	if item.Title() != "gpt-4o" {
		t.Errorf("expected title gpt-4o, got %s", item.Title())
	}
	if item.Description() != "Provider: openai" {
		t.Errorf("expected description Provider: openai, got %s", item.Description())
	}
	if item.FilterValue() != "openai gpt-4o" {
		t.Errorf("expected filter value openai gpt-4o, got %s", item.FilterValue())
	}
}

func TestNewModelPicker(t *testing.T) {
	items := []list.Item{
		ModelItem{Provider: "ollama", Model: "codellama"},
		ModelItem{Provider: "anthropic", Model: "claude-sonnet-4-20250514"},
	}

	picker := NewModelPicker(items, 80, 24)
	if len(picker.Items()) != 2 {
		t.Errorf("expected 2 items in picker, got %d", len(picker.Items()))
	}
}
