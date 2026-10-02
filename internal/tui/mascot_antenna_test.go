package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func zzClock() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func lipglossWidth(s string) int { return lipgloss.Width(s) }

// The antenna has to sit directly above the ┴ in the lid. padTo centres a
// glyph within the whole portrait width, which includes the two-space indent,
// so it put the stalk one cell left of the joint — the antenna looked
// detached from the box it was supposed to sprout from.
//
// Columns are measured in display cells, not runes: the mouth is a
// presentation-form glyph that both terminals and lipgloss count as two.
func TestHeroAntennaSitsAboveTheLidJoint(t *testing.T) {
	for _, off := range []int{-1, 0, 1} {
		m := newMascot()
		m.amp = 1
		m.bob.pos = float64(off)

		rows := strings.Split(stripANSI(heroOf(m)), "\n")

		lidRow := -1
		for i, row := range rows {
			if strings.ContainsRune(row, '╭') {
				lidRow = i
				break
			}
		}
		if lidRow < 0 {
			t.Fatalf("bob %+d: no ╭ in the portrait, the lid is missing", off)
		}

		jointCol := displayCols(rows[lidRow], '┴')
		if len(jointCol) != 1 {
			t.Fatalf("bob %+d: lid has %d ┴ joints, want exactly 1", off, len(jointCol))
		}
		want := jointCol[0]
		// The ┴ is the middle of the lid: ╭──┴──╮.
		l, r := displayCols(rows[lidRow], '╭'), displayCols(rows[lidRow], '╮')
		if len(l) == 1 && len(r) == 1 && l[0]+r[0] != 2*want {
			t.Errorf("bob %+d: lid corners at %d and %d are not symmetric about the ┴ at %d",
				off, l[0], r[0], want)
		}

		// Every non-blank row above the lid is antenna, and each must line up
		// with the joint.
		antennaRows := 0
		for i := 0; i < lidRow; i++ {
			cols := nonSpaceCols(rows[i])
			if len(cols) == 0 {
				continue
			}
			antennaRows++
			if len(cols) != 1 {
				t.Errorf("bob %+d: antenna row %d has %d glyphs, want 1", off, i, len(cols))
				continue
			}
			if cols[0] != want {
				t.Errorf("bob %+d: antenna row %d sits at cell %d, want %d (the ┴ column)",
					off, i, cols[0], want)
			}
		}
		if antennaRows != 2 {
			t.Errorf("bob %+d: found %d antenna rows, want 2 (the tip and the stalk)",
				off, antennaRows)
		}
	}
}

// nonSpaceCols returns the display columns of every non-space rune in s.
func nonSpaceCols(s string) []int {
	var out []int
	col := 0
	for _, r := range s {
		if r != ' ' {
			out = append(out, col)
		}
		col += lipgloss.Width(string(r))
	}
	return out
}

// displayCols returns the display columns of every occurrence of target.
func displayCols(s string, target rune) []int {
	var out []int
	col := 0
	for _, r := range s {
		if r == target {
			out = append(out, col)
		}
		col += lipgloss.Width(string(r))
	}
	return out
}

// heroOf renders the portrait with the deterministic test clock.
func heroOf(m mascot) string {
	return m.hero(zzClock(), mascotFaceStyles)
}

// Every row must be the same width at every bob offset, and the antenna rows
// must match the box they sit on.
func TestHeroAntennaRowsAreWidthMatched(t *testing.T) {
	for _, off := range []int{-1, 0, 1} {
		m := newMascot()
		m.amp = 1
		m.bob.pos = float64(off)

		var width int
		for i, row := range strings.Split(stripANSI(heroOf(m)), "\n") {
			if w := lipglossWidth(row); w != width {
				if width == 0 {
					width = w
					continue
				}
				t.Errorf("bob %+d: row %d is %d cells, want %d", off, i, w, width)
			}
		}
	}
}
