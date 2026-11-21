package tui

import (
	"strings"
	"testing"
)

func TestLayoutGeometry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		w, h          int
		compact       bool
		sidebar, chat int
	}{
		{"wide", 140, 40, false, 32, 102},
		{"narrow", 119, 40, true, 0, 115},
		{"short", 140, 29, true, 0, 136},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := calculateLayout(tc.w, tc.h)
			if g.compact != tc.compact || g.sidebarWidth != tc.sidebar || g.chatWidth != tc.chat {
				t.Fatalf("geometry = %+v", g)
			}
		})
	}
}

func TestPromptColumnHasFourCells(t *testing.T) {
	for _, badge := range []bool{false, true} {
		if got := promptColumn(badge); got != "::: " && got != " !  " {
			t.Fatalf("badge=%v prompt=%q", badge, got)
		}
		if got := cellWidth(promptColumn(badge)); got != 4 {
			t.Fatalf("badge=%v width=%d", badge, got)
		}
	}
}

func TestChatGutterAndContentCap(t *testing.T) {
	m := Model{width: 200, height: 40, focusedItem: -1}
	m.messages = []ChatMessage{{Role: RoleUser, Label: "you", Content: "hello"}, {Role: RoleAssistant, Label: "GoCode", Content: "world"}}
	got := m.renderMessages()
	if !containsAll(got, "│ ") || cellWidth(m.renderChatItem(m.messages[0], true)) > 122 {
		t.Fatalf("chat render missing two-cell gutter/content cap: %q", got)
	}
}

func TestFocusChangeInvalidatesOnlyFocusedBlocks(t *testing.T) {
	m := Model{width: 140, focusedItem: 0, messages: []ChatMessage{{Role: RoleUser, Content: "a"}, {Role: RoleAssistant, Content: "b"}, {Role: RoleTool, Content: "c"}}}
	m.renderMessages()
	before := m.cache.renders
	m.focusedItem = 1
	m.renderMessages()
	if got := m.cache.renders - before; got != 2 {
		t.Fatalf("focus change rendered %d blocks, want old and new focused blocks only", got)
	}
}

func TestCompactTransitionInvalidatesRenderCache(t *testing.T) {
	m := Model{width: 140, height: 40, focusedItem: -1, messages: []ChatMessage{{Role: RoleUser, Content: "a"}}}
	m.renderMessages()
	m.width = 119
	was, now := m.updateLayoutEpoch(140, 40, 119, 40)
	if was || !now || len(m.cache.blocks) != 0 {
		t.Fatalf("compact transition did not invalidate: was=%v now=%v blocks=%d", was, now, len(m.cache.blocks))
	}
	m.renderMessages()
	was, now = m.updateLayoutEpoch(140, 40, 140, 29)
	if was || !now || len(m.cache.blocks) != 0 {
		t.Fatalf("height-driven compact transition did not invalidate: was=%v now=%v blocks=%d", was, now, len(m.cache.blocks))
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
