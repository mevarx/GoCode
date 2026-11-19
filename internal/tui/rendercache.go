package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// transcriptFlushMsg is the coalescing frame tick: streamed chunks only mark the
// transcript dirty, and one of these applies the rendered result to the
// viewport at most 30 times a second instead of once per token.
type transcriptFlushMsg struct{}

// streamFrameInterval caps transcript redraws at 30 fps. Above that the
// terminal cannot show the difference and the render cost buys nothing.
const streamFrameInterval = time.Second / 30

// renderedBlock is one message's cached render. ChatMessage is comparable, so
// struct equality is the change signal: a settled message compares equal on
// every later pass and its render (the expensive part) is reused verbatim,
// while the live assistant tail or a mutating tool card fails the comparison
// and is the only block re-rendered. This mirrors omp's reference-equality
// change signal without giving up Go value semantics.
type renderedBlock struct {
	msg      ChatMessage
	width    int
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

// lookup returns the cached render for index i when neither the message nor the
// width changed since it was stored.
func (c *renderCache) lookup(i int, msg ChatMessage, width int) (string, bool) {
	if i >= len(c.blocks) {
		return "", false
	}
	b := c.blocks[i]
	if b.msg == msg && b.width == width {
		return b.rendered, true
	}
	return "", false
}

// store records a fresh render for index i, growing the cache in message order.
func (c *renderCache) store(i int, msg ChatMessage, width int, rendered string) {
	b := renderedBlock{msg: msg, width: width, rendered: rendered}
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
		return renderBanner(m.providerName, m.modelName, m.version, m.width)
	}
	// Width is the cache epoch: a resize invalidates every block at once.
	if m.cache.width != m.width {
		m.cache.reset(m.width)
	}
	parts := make([]string, len(m.messages))
	for i, msg := range m.messages {
		if rendered, ok := m.cache.lookup(i, msg, m.width); ok {
			parts[i] = rendered
			continue
		}
		rendered := m.renderMessage(msg)
		m.cache.store(i, msg, m.width, rendered)
		parts[i] = rendered
	}
	m.cache.trim(len(m.messages))
	return strings.Join(parts, "\n")
}

// markDirty records that the rendered transcript changed but the viewport has
// not seen it yet. Streamed chunks use this instead of SetContent so N tokens
// between frames cost N appends and one viewport update.
func (m *Model) markDirty() {
	m.dirty = true
}

// armFlushTick returns a coalescing tick unless one is already in flight, so a
// burst of chunks can never stack tick chains.
func (m *Model) armFlushTick() tea.Cmd {
	if m.flushArmed {
		return nil
	}
	m.flushArmed = true
	return tea.Tick(streamFrameInterval, func(time.Time) tea.Msg {
		return transcriptFlushMsg{}
	})
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
