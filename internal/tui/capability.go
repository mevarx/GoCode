package tui

import (
	"os"
	"strings"
)

// Detects terminal capabilities once at startup so the UI degrades instead of emitting garbage.
// Values are read-only afterwards, so no locking is needed.
type caps struct {
	// NoColor when NO_COLOR is set or TERM can't render colour.
	NoColor bool
	// Set when CLICOLOR_FORCE asks for colour even on a pipe.
	ForceColor bool
	// False for pipes/files; animating into one only emits escape-sequence garbage.
	IsTTY bool
	// Set for TERM=dumb, which understands no escape sequences.
	Dumb bool
	// Set via GOCODE_REDUCED_MOTION or REDUCED_MOTION.
	ReducedMotion bool
	// Resolved in theme.go, which owns colour decisions.
	DarkBackground bool
}

var capabilities = detectCapabilities()

// Pure (no terminal queries) so it's instant and safe for tests.
func detectCapabilities() caps {
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))

	c := caps{
		NoColor:       os.Getenv("NO_COLOR") != "",
		ForceColor:    os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0",
		Dumb:          term == "dumb",
		ReducedMotion: truthy(os.Getenv("GOCODE_REDUCED_MOTION")) || truthy(os.Getenv("REDUCED_MOTION")),
	}

	c.IsTTY = isTerminal(os.Stdout)
	if c.Dumb {
		c.NoColor = true
	}
	// NO_COLOR wins over CLICOLOR_FORCE as the stronger accessibility signal.
	if c.NoColor {
		c.ForceColor = false
	}
	return c
}

// Any set-but-not-false value counts as on.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Stdlib-only isatty to avoid a dependency for one syscall wrapper.
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// motionLevel is the resolved animation policy: config intent combined with
// the terminal's own reduced-motion signal.
type motionLevel int

const (
	// motionFull: everything animates.
	motionFull motionLevel = iota
	// motionReduced: decorative motion stops; functional indicators stay.
	motionReduced
	// motionOff: as reduced, and the user asked for it explicitly.
	motionOff
)

// Motion settings accepted by [tui] motion.
const (
	MotionAuto    = "auto"
	MotionReduced = "reduced"
	MotionOff     = "off"
)

// MotionSettings is the [tui] animation configuration handed to the UI.
type MotionSettings struct {
	// Mode is "auto", "reduced" or "off"; empty behaves as "auto".
	Mode string
	// SmoothStreaming paces streamed text and tool-argument previews into the
	// transcript instead of appending raw provider bursts.
	SmoothStreaming bool
}

// resolve folds the capability probe into the configured mode. Precedence is
// off > reduced > auto: an explicit "off" wins over a capable terminal, and a
// terminal that asks for less motion always wins over "auto".
func (s MotionSettings) resolve(reducedBySystem bool) motionLevel {
	switch strings.ToLower(strings.TrimSpace(s.Mode)) {
	case MotionOff:
		return motionOff
	case MotionReduced:
		return motionReduced
	default:
		if reducedBySystem {
			return motionReduced
		}
		return motionFull
	}
}

// animMode is the live policy: read per call so a capability flip is honoured
// without rebuilding the model.
func (m *Model) animMode() motionLevel {
	return m.motion.resolve(capabilities.ReducedMotion)
}

// decorativeMotion gates effects that exist only to look alive — the shimmer
// band and the mascot's springs. Functional indicators use spinnerAnimated
// instead, which degrades them rather than removing them.
func (m *Model) decorativeMotion() bool { return m.animMode() == motionFull }

// smoothActive reports whether streamed text is paced this instant. Pacing is
// decorative: under reduced or off motion the text snaps to full.
func (m *Model) smoothActive() bool {
	return m.animMode() == motionFull && m.motion.SmoothStreaming && m.streaming
}
