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

func colorEnabled() bool {
	if capabilities.ForceColor {
		return true
	}
	return !capabilities.NoColor
}

// Off for non-TTY, dumb terminals, or reduced motion; callers must not start a ticker.
// A single static frame is always fine.
func animationsEnabled() bool {
	if capabilities.ReducedMotion || capabilities.Dumb {
		return false
	}
	// ForceColor re-enables animation for demos and tests.
	if !capabilities.IsTTY && !capabilities.ForceColor {
		return false
	}
	return true
}

// Reduced motion also shortens timings so the UI feels deliberate, not frozen.
func animationScale() float64 {
	if animationsEnabled() {
		return 1
	}
	return 0
}
