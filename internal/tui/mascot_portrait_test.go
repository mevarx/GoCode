package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// Frozen instant so blink cycles can't make assertions time-dependent.
func nowForTest() time.Time { return time.Unix(1_700_000_000, 0) }

// Mid-flight bob trims antenna, so portrait must come from rest().
func TestRestPortraitIsStableAndComplete(t *testing.T) {
	wantRows := strings.Split(stripANSI(newMascot().rest().hero(nowForTest(), mascotFaceStyles)), "\n")
	if len(wantRows) < 6 {
		t.Fatalf("portrait has %d rows, want at least 6: %q", len(wantRows), wantRows)
	}
	if strings.TrimSpace(stripANSI(wantRows[0])) != "●" {
		t.Errorf("portrait is missing its antenna tip; first row is %q", wantRows[0])
	}

	// Repeated calls must be byte-identical.
	for i := 0; i < 5; i++ {
		got := strings.Split(stripANSI(newMascot().rest().hero(nowForTest(), mascotFaceStyles)), "\n")
		if strings.Join(got, "\n") != strings.Join(wantRows, "\n") {
			t.Fatalf("portrait changed between renders:\n%q\nvs\n%q", got, wantRows)
		}
	}

	// Measure display cells, not runes: face glyphs are ambiguous width.
	base := lipgloss.Width(stripANSI(wantRows[len(wantRows)-1]))
	for i, row := range wantRows {
		if w := lipgloss.Width(stripANSI(row)); w != base {
			t.Errorf("row %d is %d cells, want %d: %q", i, w, base, row)
		}
	}
}

// rest() must neutralise motion, not just reset phase.
func TestRestClearsSpringMotion(t *testing.T) {
	m := newMascot()
	m.setState(mascotWorking, nowForTest())
	for i := 0; i < 30; i++ {
		m.step(msgNow())
	}
	if m.cellOffset() == 0 && m.bob.value() == 0 && m.sway.value() == 0 {
		t.Fatal("test is not exercising anything: the springs never moved")
	}

	r := m.rest()
	if r.bob.value() != 0 || r.bob.vel != 0 || r.sway.value() != 0 || r.sway.vel != 0 {
		t.Errorf("rest left motion in the springs: bob=%v/%v sway=%v/%v",
			r.bob.value(), r.bob.vel, r.sway.value(), r.sway.vel)
	}
	if got := r.cellOffset(); got != 0 {
		t.Errorf("rest cellOffset = %d, want 0", got)
	}
}

// Row width must hold at every offset, not only at rest.
func TestHeroRowsAlignAtEveryOffset(t *testing.T) {
	m := newMascot()
	m.setState(mascotWorking, nowForTest())

	checked := 0
	for i := 0; i < 900; i++ {
		m.step(msgNow())
		rows := strings.Split(stripANSI(m.hero(nowForTest(), mascotFaceStyles)), "\n")
		base := lipgloss.Width(rows[len(rows)-1])
		for r, row := range rows {
			if w := lipgloss.Width(row); w != base {
				t.Fatalf("frame %d row %d is %d cells, want %d: %q",
					i, r, w, base, row)
			}
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("swept no frames")
	}
}
