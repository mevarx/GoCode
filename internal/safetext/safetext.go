// Package safetext makes untrusted, model- or tool-derived text safe to print
// on a terminal. It removes control characters and ANSI escape sequences so a
// hostile string cannot repaint the screen, hide a prompt, or spoof a state.
package safetext

import (
	"strings"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
)

// Sanitize strips C0 controls (except newline and tab), DEL, C1 controls, and
// all ANSI escape forms: CSI, OSC, DCS/SOS/PM/APC, two-byte escapes, and any
// lone ESC. Printable Unicode, newlines, and tabs are preserved.
func Sanitize(s string) string {
	if !needsSanitize(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid byte; drop it rather than passing it to the terminal.
			i++
			continue
		}
		switch {
		case r == esc:
			i += escapeLen(s[i:])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
			i += size
		case r < 0x20 || r == 0x7f:
			i += size
		case r >= 0x80 && r <= 0x9f:
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

func needsSanitize(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == esc || c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			return true
		}
		if c >= 0x80 {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				return true
			}
			if r >= 0x80 && r <= 0x9f {
				return true
			}
			i += size - 1
		}
	}
	return false
}

// escapeLen reports how many bytes of s (which begins with ESC) to consume.
func escapeLen(s string) int {
	if len(s) < 2 {
		return 1
	}
	switch s[1] {
	case '[':
		// CSI: consume parameter/intermediate bytes through the final 0x40–0x7E.
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']':
		// OSC: terminated by BEL or ST (ESC \).
		for i := 2; i < len(s); i++ {
			switch s[i] {
			case bel:
				return i + 1
			case esc:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
			}
		}
		return len(s)
	case 'P', 'X', '^', '_':
		// DCS, SOS, PM, APC: terminated by ST (ESC \); BEL tolerated as a stop.
		for i := 2; i < len(s); i++ {
			switch s[i] {
			case bel:
				return i + 1
			case esc:
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
			}
		}
		return len(s)
	default:
		// Two-byte escape: ESC plus one byte.
		return 2
	}
}
