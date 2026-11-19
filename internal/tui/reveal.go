package tui

import (
	"time"
	"unicode"
	"unicode/utf8"
)

// The reveal law, ported from omp: a burst of streamed text is spread across
// frames instead of landing as one wall of words. Each 30 fps tick advances
// max(3, ceil(backlog/8)) clusters, so a slow stream still moves at least
// three units and a large backlog catches up in well under a second without a
// budget that depends on message length.
const (
	revealMinStep       = 3
	revealCatchupFrames = 8
)

// revealStep is the number of clusters one tick may reveal for a backlog.
func revealStep(backlog int) int {
	step := revealMinStep
	if backlog > revealMinStep {
		if catchup := (backlog + revealCatchupFrames - 1) / revealCatchupFrames; catchup > step {
			step = catchup
		}
	}
	return step
}

// revealBacklog counts the clusters still hidden after shown.
func revealBacklog(s string, shown int) int {
	if shown < 0 {
		shown = 0
	}
	n := 0
	for i := shown; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		// Combining marks belong to the cluster before them, so they do not
		// count as units of their own.
		if !unicode.IsMark(r) {
			n++
		}
	}
	return n
}

// clusterEnd returns the offset just past the cluster starting at i: one base
// rune plus every combining mark that follows it. Slicing only at cluster
// boundaries is what stops a tick from splitting an accent from its letter.
func clusterEnd(s string, i int) int {
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		return len(s)
	}
	_, size := utf8.DecodeRuneInString(s[i:])
	i += size
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsMark(r) {
			break
		}
		i += sz
	}
	return i
}

// nextRevealStop advances one tick from shown, stopping on a cluster boundary.
func nextRevealStop(s string, shown int) int {
	if shown < 0 {
		shown = 0
	}
	if shown >= len(s) {
		return len(s)
	}
	stop := shown
	for taken := 0; taken < revealStep(revealBacklog(s, shown)) && stop < len(s); taken++ {
		stop = clusterEnd(s, stop)
	}
	return stop
}

// revealPrefix decides how much of target the transcript should show and
// returns the new offset. Pacing only exists inside a live turn: when the
// stream ends, when smooth_streaming is off, or under reduced motion the
// prefix is the whole target, so text snaps to full instead of trickling.
func (m *Model) revealPrefix(target string, shown int, advance bool) (string, int) {
	if shown > len(target) {
		shown = len(target)
	}
	if shown < 0 {
		shown = 0
	}
	if !m.smoothActive() {
		return target, len(target)
	}
	if advance {
		shown = nextRevealStop(target, shown)
	}
	return target[:shown], shown
}

// revealPending reports whether paced text still has a backlog to show.
func (m *Model) revealPending() bool {
	if !m.smoothActive() {
		return false
	}
	if m.streamBuf.Len() > m.revealShown {
		return true
	}
	for _, card := range m.pendingTools {
		if len(card.args) > card.shown {
			return true
		}
	}
	return false
}

// cardElapsed is the elapsed badge value. A card without a start time reads as
// freshly started rather than as the zero Time's 292 years.
func cardElapsed(since, now time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return now.Sub(since)
}

// refreshLiveContent recomputes every live widget's text from the shared clock
// and the reveal buffers: the assistant tail's paced prefix, and each pending
// card's spinner glyph, elapsed badge and revealed argument preview.
//
// advance=false only adopts text that just arrived (a chunk grows the buffer;
// the next tick reveals it); advance=true performs one reveal step. It reports
// whether any transcript content changed, so the caller can mark the viewport
// dirty once per frame instead of once per widget.
func (m *Model) refreshLiveContent(now time.Time, advance bool) bool {
	changed := false

	if m.streamBuf.Len() > 0 {
		prefix, next := m.revealPrefix(m.streamBuf.String(), m.revealShown, advance)
		m.revealShown = next
		if m.updateStreamingTail(prefix) {
			changed = true
		}
	}

	if len(m.pendingTools) == 0 {
		return changed
	}
	glyph := m.spinnerGlyph(now)
	for id, card := range m.pendingTools {
		if card.index < 0 || card.index >= len(m.messages) {
			delete(m.pendingTools, id)
			continue
		}
		prefix, next := m.revealPrefix(card.args, card.shown, advance)
		card.shown = next
		content := toolCardContent(card.status, glyph, cardElapsed(card.since, now), prefix)
		if m.messages[card.index].Content != content {
			m.messages[card.index].Content = content
			changed = true
		}
	}
	return changed
}
