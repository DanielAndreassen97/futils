package ui

import (
	"strings"
	"testing"
)

// countCells counts the filled (▰) and empty (▱) runes in a RenderBar output,
// ignoring the surrounding ANSI color codes.
func countCells(s string) (filled, empty int) {
	filled = strings.Count(s, "▰")
	empty = strings.Count(s, "▱")
	return
}

func TestRenderBarEmpty(t *testing.T) {
	f, e := countCells(RenderBar(0, 8))
	if f != 0 || e != 8 {
		t.Errorf("frac=0 width=8: filled=%d empty=%d, want 0/8", f, e)
	}
}

func TestRenderBarFull(t *testing.T) {
	f, e := countCells(RenderBar(1, 8))
	if f != 8 || e != 0 {
		t.Errorf("frac=1 width=8: filled=%d empty=%d, want 8/0", f, e)
	}
}

func TestRenderBarHalf(t *testing.T) {
	f, e := countCells(RenderBar(0.5, 8))
	if f != 4 || e != 4 {
		t.Errorf("frac=0.5 width=8: filled=%d empty=%d, want 4/4", f, e)
	}
}

func TestRenderBarClampsOutOfRange(t *testing.T) {
	if f, e := countCells(RenderBar(-0.5, 8)); f != 0 || e != 8 {
		t.Errorf("frac=-0.5: filled=%d empty=%d, want 0/8", f, e)
	}
	if f, e := countCells(RenderBar(2, 8)); f != 8 || e != 0 {
		t.Errorf("frac=2: filled=%d empty=%d, want 8/0", f, e)
	}
}

func TestRenderBarTotalWidth(t *testing.T) {
	for _, frac := range []float64{0, 0.1, 0.3, 0.5, 0.7, 0.9, 1} {
		f, e := countCells(RenderBar(frac, 10))
		if f+e != 10 {
			t.Errorf("frac=%v: filled+empty=%d, want 10", frac, f+e)
		}
	}
}
