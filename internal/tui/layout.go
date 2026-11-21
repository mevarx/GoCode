package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/mevarx/GoCode/internal/tools"
)

type layoutGeometry struct {
	width, height           int
	compact                 bool
	chatWidth, sidebarWidth int
}

func calculateLayout(width, height int) layoutGeometry {
	width, height = max(1, width), max(1, height)
	g := layoutGeometry{width: width, height: height, compact: width < 120 || height < 30}
	if g.compact {
		g.chatWidth = max(1, width-4) // outer frame + inner breathing room
		return g
	}
	g.sidebarWidth = 32
	g.chatWidth = max(1, width-2 /* frame */ -g.sidebarWidth-2 /* gutter */ -2 /* chat insets */)
	return g
}

func cellWidth(s string) int { return lipgloss.Width(s) }

func promptColumn(badge bool) string {
	if badge {
		return " !  "
	}
	return "::: "
}
func (m *Model) updateLayoutEpoch(oldWidth, oldHeight, newWidth, newHeight int) (wasCompact, nowCompact bool) {
	wasCompact = calculateLayout(oldWidth, oldHeight).compact
	nowCompact = calculateLayout(newWidth, newHeight).compact
	if wasCompact != nowCompact {
		m.cache.invalidate()
	}
	return
}

func gradientWordmark(width int) string {
	word := "GoCode"
	if width < len(word) {
		word = word[:width]
	}
	var b strings.Builder
	for i, r := range word {
		c := lipgloss.Blend1D(max(2, len(word)), activeTheme.Tokens.Secondary, activeTheme.Tokens.Primary)[i]
		b.WriteString(lipgloss.NewStyle().Foreground(c).Bold(true).Render(string(r)))
	}
	return b.String()
}

func (m *Model) renderLogo(rows, width int) string {
	if rows <= 0 || width <= 0 {
		return ""
	}
	parts := make([]string, rows)
	logo := gradientWordmark(max(1, min(width, 6)))
	field := strings.Repeat("╱", max(1, width-cellWidth(logo)-1))
	for i := range parts {
		switch i {
		case 0, 1, rows - 2:
			parts[i] = lipgloss.NewStyle().Foreground(activeTheme.Tokens.Primary).Render(truncateToWidth(field, width))
		case 2:
			parts[i] = truncateToWidth(" GoCode", width)
		case 3, 4, 5:
			parts[i] = truncateToWidth(" "+logo, width)
		default:
			parts[i] = ""
		}
	}
	return strings.Join(parts, "\n")
}

// sidebarScrollStep is the sidebar's scroll granularity (:
// four lines per up/down step).
const sidebarScrollStep = 4

// sidebarLines builds the full sidebar content (logo + data sections) without
// width truncation or the scroll window, so render and max-offset agree.
func (m *Model) sidebarLines() []string {
	width := calculateLayout(m.width, m.height).sidebarWidth - 1
	height := max(1, m.height-7)
	lines := strings.Split(m.renderLogo(min(8, height), width), "\n")
	sections := []string{"", m.modelName + " via " + m.providerName, filepath.Base(m.workspaceRoot)}
	if m.lastTurn.PromptTokens > 0 {
		sections = append(sections, m.renderTurnMetrics())
	}
	// data wiring: session identity, configured MCP servers and the live
	// plan summary. Each section is dropped wholesale when its data is absent.
	if m.sidebar.SessionID != "" {
		sections = append(sections, "", "session "+m.sidebar.SessionID)
		if m.sidebar.SessionTitle != "" {
			sections = append(sections, m.sidebar.SessionTitle)
		}
	}
	if len(m.sidebar.MCPServers) > 0 {
		sections = append(sections, "", "mcp")
		for i, name := range m.sidebar.MCPServers {
			if i == 3 {
				sections = append(sections, fmt.Sprintf("+%d more", len(m.sidebar.MCPServers)-3))
				break
			}
			sections = append(sections, name)
		}
	}
	if m.planStore != nil {
		if items := m.planStore.Items(); len(items) > 0 {
			done := 0
			for _, it := range items {
				if it.Status == tools.PlanStatusDone {
					done++
				}
			}
			sections = append(sections, "", "plan", fmt.Sprintf("%d/%d done", done, len(items)))
		}
	}
	lines = append(lines, sections...)
	return lines
}

func (m *Model) renderSidebar(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := m.sidebarLines()
	for i := range lines {
		lines[i] = truncateToWidth(lines[i], width)
	}
	off := min(m.sidebarOffset, max(0, len(lines)-height))
	if off > 0 {
		lines = lines[off:]
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

// sidebarMaxOffset is the furthest the sidebar can scroll (0 in compact mode).
func (m *Model) sidebarMaxOffset() int {
	g := calculateLayout(m.width, m.height)
	if g.compact {
		return 0
	}
	height := max(1, m.height-7)
	return max(0, len(m.sidebarLines())-height)
}

func (m *Model) scrollSidebar(delta int) {
	m.sidebarOffset = min(m.sidebarMaxOffset(), max(0, m.sidebarOffset+delta))
}

func (m *Model) renderCompactHeader(width int) string {
	path := workspaceShortName(m.workspaceRoot)
	parts := []string{path}
	if pct, ok := m.lastTurn.ContextPercent(); ok {
		parts = append(parts, "ctx "+formatTokenCount(int(pct))+"%")
	}
	parts = append(parts, "ctrl+c quit")
	left := " " + gradientWordmark(width) + " ╱╱╱ "
	return truncateToWidth(left+strings.Join(parts, " • "), width)
}

func gutter(role ChatRole, focused bool) string {
	if focused {
		color := activeTheme.Tokens.SuccessMostSubtle
		if role == RoleUser {
			color = activeTheme.Tokens.Primary
		}
		return lipgloss.NewStyle().Foreground(color).Render("▌ ")
	}
	if role == RoleUser {
		return lipgloss.NewStyle().Foreground(activeTheme.Tokens.Primary).Render("│ ")
	}
	return "  "
}

func (m *Model) renderChatItem(msg ChatMessage, focused bool) string {
	return m.renderChatItemView(-1, msg, focused)
}

// renderChatItemView renders one transcript item with its per-index view state:
// long items collapse to the first collapseLines lines plus an affordance, and
// H/L horizontal scroll shifts the content. i < 0 renders the plain base item
// (used by direct renderChatItem callers and tests).
func (m *Model) renderChatItemView(i int, msg ChatMessage, focused bool) string {
	body := m.renderMessage(msg)
	body = strings.TrimRight(body, "\n")
	capWidth := min(120, max(1, m.chatContentWidth()-2))
	lines := strings.Split(body, "\n")
	if i >= 0 && !m.expanded[i] && len(lines) > collapseLines && !m.liveTail(i, msg) {
		hidden := len(lines) - collapseLines
		lines = append(lines[:collapseLines], collapseAffordance(hidden))
	}
	off := 0
	if i >= 0 {
		off = m.hOffset[i]
	}
	prefix := gutter(msg.Role, focused)
	out := make([]string, len(lines))
	for j, line := range lines {
		if off > 0 {
			line = sliceRunesFrom(line, off)
		}
		line = truncateToWidth(line, capWidth)
		out[j] = prefix + line
	}
	return strings.Join(out, "\n")
}

// liveTail is the streaming assistant block: never collapsed, so new tokens stay
// visible while a turn runs.
func (m *Model) liveTail(i int, msg ChatMessage) bool {
	return m.streaming && i == len(m.messages)-1 && msg.Role == RoleAssistant
}

func sliceRunesFrom(s string, off int) string {
	r := []rune(s)
	if off >= len(r) {
		return ""
	}
	return string(r[off:])
}

func (m *Model) chatContentWidth() int { return calculateLayout(m.width, m.height).chatWidth }
