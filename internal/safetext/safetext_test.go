package safetext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"csi clear screen", "\x1b[2J", ""},
		{"csi cursor home", "\x1b[1;1H", ""},
		{"csi color then text", "\x1b[31mApprove? [y/N]\x1b[0m", "Approve? [y/N]"},
		{"osc 52 clipboard", "\x1b]52;c;SGVsbG8=\x07", ""},
		{"osc terminated by st", "\x1b]0;title\x1b\\kept", "kept"},
		{"dcs", "\x1bPq#0;1;0;0;0;0\x1b\\after", "after"},
		{"sos", "\x1bXpayload\x1b\\", ""},
		{"pm", "\x1b^payload\x1b\\", ""},
		{"apc", "\x1b_payload\x1b\\", ""},
		{"lone esc", "safe\x1b", "safe"},
		{"two byte escape", "a\x1b7b", "ab"},
		{"c1 csi", "\xc2\x9b31mInjected", "31mInjected"},
		{"del", "a\x7fb", "ab"},
		{"cr and bell", "line\rbell\x07", "linebell"},
		{"newline and tab kept", "a\nb\tc", "a\nb\tc"},
		{"unicode kept", "日本語 🚀 café", "日本語 🚀 café"},
		{"spoofed prompt stays data", "\x1b[2JApprove? [y/N]", "Approve? [y/N]"},
		{"invalid utf8 dropped", "a\xffb", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			assertPrintable(t, got)
		})
	}
}

func TestSanitizePreservesVisibleText(t *testing.T) {
	in := "\x1b[2J\x1b[1;1HApprove? [y/N]\x1b]52;c;SGVsbG8=\x07"
	got := Sanitize(in)
	if !strings.Contains(got, "Approve? [y/N]") {
		t.Fatalf("visible prompt text lost: %q", got)
	}
	if strings.ContainsRune(got, esc) {
		t.Fatalf("output still contains ESC: %q", got)
	}
}

func assertPrintable(t *testing.T, s string) {
	t.Helper()
	for i, r := range s {
		switch {
		case r == '\n' || r == '\t':
			continue
		case r == esc || r < 0x20 || r == 0x7f:
			t.Fatalf("control U+%04X at byte %d in %q", r, i, s)
		case r >= 0x80 && r <= 0x9f:
			t.Fatalf("C1 control U+%04X at byte %d in %q", r, i, s)
		case r == utf8.RuneError:
			t.Fatalf("replacement rune at byte %d in %q", i, s)
		}
	}
}
