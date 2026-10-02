package tui

import (
	"math"
	"testing"
)

// waveAt's comment documents its shape precisely, so these assertions pin that
// shape: editing either one should have to change the other deliberately.
func TestWaveAtIsAnUnsmoothedTriangle(t *testing.T) {
	const samples = 10000
	step := 1.0 / samples

	// No dwell at the extremes: the wave moves on every step away from the
	// turning points. A step straddling a corner can legitimately repeat, so
	// those are excluded.
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

	// Constant slope. Raw deltas run into float64 resolution here — the wave
	// moves 4e-5 per step against order-1 values — so divide by the step first.
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
	// A true triangle moves at a constant rate on both slopes.
	if (hi-lo)/hi > 0.01 {
		t.Errorf("slope varies from %.3f to %.3f across the cycle; the comment says constant",
			lo, hi)
	}
}

// The turning point at phase 0.5 is a sharp reversal: the wave reaches +1 and
// immediately heads back down. Nothing interpolates the corner, which is what
// makes this a triangle rather than a curve.
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
	// The descent begins immediately: one epsilon past the peak the value has
	// already dropped measurably, rather than easing out gradually.
	if drop := at - after; drop <= 0 {
		t.Errorf("no descent past the peak: %v -> %v -> %v", before, at, after)
	}
}
