package tui

import (
	"math"
	"testing"
)

// Pins documented triangle shape.
func TestWaveAtIsAnUnsmoothedTriangle(t *testing.T) {
	const samples = 10000
	step := 1.0 / samples

	// No dwell at extremes: wave moves on every step away from corners.
	repeatsAwayFromCorners := 0
	const cornerPhase = 0.5
	for i := 1; i <= samples; i++ {
		p := float64(i) * step
		if math.Abs(p-cornerPhase) < 2*step || math.Abs(p-1) < 2*step {
			continue
		}
		if waveAt(p) == waveAt(float64(i-1)*step) {
			repeatsAwayFromCorners++
		}
	}
	if repeatsAwayFromCorners != 0 {
		t.Errorf("%d samples repeated away from the turning points; the comment says no dwell",
			repeatsAwayFromCorners)
	}

	// Constant slope; divide by step first for float64 resolution.
	var lo, hi = math.Inf(1), 0.0
	prev := waveAt(0)
	for i := 1; i <= samples; i++ {
		v := waveAt(float64(i) * step)
		slope := math.Abs(v-prev) / step
		if slope < lo {
			lo = slope
		}
		if slope > hi {
			hi = slope
		}
		prev = v
	}
	if hi == 0 {
		t.Fatal("the wave never changed; it is not a ramp")
	}
	if lo == 0 {
		t.Fatal("some steps produced no measurable change away from the corners")
	}
	// True triangle moves at constant rate on both slopes.
	if (hi-lo)/hi > 0.01 {
		t.Errorf("slope varies from %.3f to %.3f across the cycle; the comment says constant",
			lo, hi)
	}
}

// Turning point at 0.5 is a sharp reversal, not an eased curve.
func TestWaveAtReversesSharplyAtTheTurningPoint(t *testing.T) {
	const eps = 1e-9
	before := waveAt(0.5 - eps)
	at := waveAt(0.5)
	after := waveAt(0.5 + eps)

	if math.Abs(at-1) > 1e-9 {
		t.Errorf("waveAt(0.5) = %v, want +1", at)
	}
	if !(before < at && after < at) {
		t.Errorf("expected a peak at 0.5, got %v -> %v -> %v", before, at, after)
	}
	// Descent begins immediately past the peak.
	if drop := at - after; drop <= 0 {
		t.Errorf("no descent past the peak: %v -> %v -> %v", before, at, after)
	}
}
