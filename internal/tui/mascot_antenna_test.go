package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func zzClock() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }

func lipglossWidth(s string) int { return lipgloss.Width(s) }

// Antenna must sit above the ┴ joint; padTo incl. indent lands one cell left.
// Columns measured in display cells, not runes.
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
		l, r := displayCols(rows[lidRow], '╭'), displayCols(rows[lidRow], '╮')
		if len(l) == 1 && len(r) == 1 && l[0]+r[0] != 2*want {
			t.Errorf("bob %+d: lid corners at %d and %d are not symmetric about the ┴ at %d",
				off, l[0], r[0], want)
		}

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

func heroOf(m mascot) string {
	return m.hero(zzClock(), mascotFaceStyles)
}

// Rows must share width at every bob offset.
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
