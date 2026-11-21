package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// renderedBlock is one message's cached render. ChatMessage is comparable, so
// struct equality is the change signal: a settled message compares equal on
// every later pass and its render (the expensive part) is reused verbatim,
// while the live assistant tail or a mutating tool card fails the comparison
// and is the only block re-rendered. This uses reference-equality
// change signal without giving up Go value semantics.
type renderedBlock struct {
	msg      ChatMessage
	width    int
	focused  bool
	expanded bool
	hOffset  int
	height   int
	rendered string
}

// renderCache holds rendered transcript blocks in message order. It is a value
// field on Model so it travels with the Bubble Tea model copy; the backing
// array is shared through the slice header.
type renderCache struct {
	blocks []renderedBlock
	// width is the epoch the cached renders belong to. A resize invalidates
	// every block, because wrapping depends on the width.
	width int
	// renders counts real block renders. It is the seam the cache tests assert
	// against; production never reads it.
	renders int
}

// reset drops every block, e.g. when the width epoch changes.
func (c *renderCache) reset(width int) {
	c.blocks = c.blocks[:0]
	c.width = width
}

// invalidate drops every cached render without adopting a new width. A theme
// switch recolours text at the same width, so the width epoch must not be
// mistaken for a resize — but the cached strings hold the old colours and are
// unusable.
func (c *renderCache) invalidate() {
	c.blocks = c.blocks[:0]
	c.width = -1
}

// lookup returns the cached render for index i when neither the message nor the
// width changed since it was stored.
func (c *renderCache) lookup(i int, msg ChatMessage, width int, focused, expanded bool, hOffset int) (string, bool) {
	if i >= len(c.blocks) {
		return "", false
	}
	b := c.blocks[i]
	if b.msg == msg && b.width == width && b.focused == focused && b.expanded == expanded && b.hOffset == hOffset {
		return b.rendered, true
	}
	return "", false
}

// store records a fresh render for index i, growing the cache in message order.
func (c *renderCache) store(i int, msg ChatMessage, width int, focused, expanded bool, hOffset int, rendered string) {
	b := renderedBlock{msg: msg, width: width, focused: focused, expanded: expanded, hOffset: hOffset, height: lipgloss.Height(rendered), rendered: rendered}
	c.renders++
	if i < len(c.blocks) {
		c.blocks[i] = b
		return
	}
	c.blocks = append(c.blocks, b)
}

// trim drops blocks for messages that no longer exist.
func (c *renderCache) trim(n int) {
	if n >= len(c.blocks) {
		return
	}
	c.blocks = c.blocks[:n]
}

// renderMessages joins one cached block per message. Settled messages hit the
// cache; only blocks whose message value changed since the last pass render.
func (m *Model) renderMessages() string {
	if len(m.messages) == 0 {
		m.cache.trim(0)
		m.spans = nil
		return renderBanner(m.providerName, m.modelName, m.version, m.width)
	}
	// Width is the cache epoch: a resize invalidates every block at once.
	if m.cache.width != m.width {
		m.cache.reset(m.width)
	}
	focus := m.focusedItem
	if focus >= len(m.messages) {
		focus = len(m.messages) - 1
	}
	// focus < 0 means no highlight (esc cleared the selection).
	parts := make([]string, len(m.messages))
	spans := make([]itemSpan, len(m.messages))
	start := 0
	for i, msg := range m.messages {
		focused := i == focus
		expanded := m.expanded[i]
		off := m.hOffset[i]
		var rendered string
		if r, ok := m.cache.lookup(i, msg, m.width, focused, expanded, off); ok {
			rendered = r
		} else {
			rendered = m.renderChatItemView(i, msg, focused)
			m.cache.store(i, msg, m.width, focused, expanded, off, rendered)
		}
		parts[i] = rendered
		// Height is cached with the block, so a settled item costs no re-scan.
		h := m.cache.blocks[i].height
		spans[i] = itemSpan{start: start, lines: h}
		start += h
	}
	m.spans = spans
	m.cache.trim(len(m.messages))
	return strings.Join(parts, "\n")
}

// markDirty records that the rendered transcript changed but the viewport has
// not seen it yet. Streamed chunks use this instead of SetContent so N tokens
// between frames cost N appends and one viewport update, applied by the shared
// clock tick (animclock.go).
func (m *Model) markDirty() {
	m.dirty = true
}

// flushTranscript applies the cached transcript to the viewport. follow keeps
// the original autoscroll contract: only a viewport already at the bottom
// moves, so a user who scrolled up stays where they are.
func (m *Model) flushTranscript(follow bool) {
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderMessages())
	m.dirty = false
	m.viewportFlushes++
	if follow && atBottom {
		m.viewport.GotoBottom()
	}
}

// flushNow applies the transcript immediately for discrete events (tool start,
// tool result, notices, turn end): they belong to the next frame, not the one
// after that. A pending coalescing tick still fires and finds nothing to do.
func (m *Model) flushNow() {
	m.flushTranscript(false)
}
