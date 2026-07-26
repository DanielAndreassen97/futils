package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const (
	glowFG   = lipgloss.Color("#eaf2f3")
	glowPeak = lipgloss.Color("#4a626b")
	glowBase = lipgloss.Color("#26343a")
)

func TestGlowBarIsExactlyWidthColumns(t *testing.T) {
	// The bar only reads as a bar if it fills the row. Short content pads,
	// long content truncates.
	for _, s := range []string{"", "short", strings.Repeat("x", 200)} {
		if got := lipgloss.Width(GlowBar(s, 40, glowFG, glowPeak, glowBase)); got != 40 {
			t.Errorf("GlowBar(%d chars) is %d columns, want 40", len(s), got)
		}
	}
}

func TestGlowBarKeepsTheText(t *testing.T) {
	got := GlowBar("DW - Core", 40, glowFG, glowPeak, glowBase)
	if !strings.Contains(got, "DW - Core") {
		t.Errorf("GlowBar dropped its text: %q", got)
	}
}

func TestGlowBarCountsRunesNotBytes(t *testing.T) {
	// Same reason the picker uses FitWidth: æ is one column, not two.
	if got := lipgloss.Width(GlowBar("DW - Ærlig & Øst", 44, glowFG, glowPeak, glowBase)); got != 44 {
		t.Errorf("bar is %d columns, want 44", got)
	}
}

func TestGlowBarZeroWidthIsEmpty(t *testing.T) {
	if got := GlowBar("anything", 0, glowFG, glowPeak, glowBase); got != "" {
		t.Errorf("GlowBar with no width = %q, want empty", got)
	}
}

func TestGlowBarFallsBackForNonHexColours(t *testing.T) {
	// ANSI palette entries are valid lipgloss colours but have no RGB to
	// interpolate. The bar must still come out the right width.
	got := GlowBar("row", 30, glowFG, lipgloss.Color("8"), lipgloss.Color("0"))
	if lipgloss.Width(got) != 30 {
		t.Errorf("fallback bar is %d columns, want 30", lipgloss.Width(got))
	}
	if !strings.Contains(got, "row") {
		t.Errorf("fallback bar dropped its text: %q", got)
	}
}

func TestHexRGB(t *testing.T) {
	r, g, b, ok := hexRGB("#4a626b")
	if !ok || r != 0x4a || g != 0x62 || b != 0x6b {
		t.Errorf("hexRGB = %d,%d,%d,%v", r, g, b, ok)
	}
	for _, bad := range []lipgloss.Color{"8", "#abc", "", "#gggggg", "4a626b"} {
		if _, _, _, ok := hexRGB(bad); ok {
			t.Errorf("hexRGB(%q) reported success", bad)
		}
	}
}

func TestLerpIntHitsBothEnds(t *testing.T) {
	if got := lerpInt(10, 20, 0); got != 10 {
		t.Errorf("lerpInt at t=0 = %d, want 10", got)
	}
	if got := lerpInt(10, 20, 1); got != 20 {
		t.Errorf("lerpInt at t=1 = %d, want 20", got)
	}
	// Rounds rather than truncating, so a fade does not drift dark.
	if got := lerpInt(0, 10, 0.55); got != 6 {
		t.Errorf("lerpInt at t=0.55 = %d, want 6", got)
	}
}

func TestGlowBarActuallyFadesAcrossTheRow(t *testing.T) {
	// The failure mode this guards is silent: swap the per-cell loop for a
	// single styled Render and every width assertion above still passes, but
	// the "glow" is a flat block. Force a truecolor profile (tests otherwise
	// run with colour stripped) and read the emitted backgrounds back out.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	out := GlowBar("DW - Core", 40, glowFG, glowPeak, glowBase)
	cells := regexp.MustCompile(`48;2;(\d+);(\d+);(\d+)`).FindAllStringSubmatch(out, -1)
	if len(cells) != 40 {
		t.Fatalf("%d cells carry a background, want one per column", len(cells))
	}
	if cells[0][1] == cells[len(cells)-1][1] {
		t.Errorf("both ends have red channel %s — the bar does not fade", cells[0][1])
	}
}
