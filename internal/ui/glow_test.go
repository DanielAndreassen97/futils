package ui

import (
	"regexp"
	"strconv"
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
	cols := columnColours(t, out)
	if len(cols) != 40 {
		t.Fatalf("%d columns carry a background, want one per column", len(cols))
	}
	if cols[0] == cols[len(cols)-1] {
		t.Errorf("both ends are %v — the bar does not fade", cols[0])
	}
}

// runPattern captures a background colour and the text painted with it, so a
// test can expand the emitted runs back into per-column colours. Asserting on
// columns rather than on escape sequences keeps these tests independent of how
// aggressively paintRuns merges neighbours.
var runPattern = regexp.MustCompile(`48;2;(\d+);(\d+);(\d+)[0-9;]*m([^\x1b]*)`)

// columnColours expands a rendered bar into one RGB triple per visible column.
func columnColours(t *testing.T, bar string) [][3]int {
	t.Helper()
	var cols [][3]int
	for _, m := range runPattern.FindAllStringSubmatch(bar, -1) {
		var rgb [3]int
		for i := range rgb {
			rgb[i], _ = strconv.Atoi(m[i+1])
		}
		for range []rune(m[4]) {
			cols = append(cols, rgb)
		}
	}
	return cols
}

// brightestColumn reports which column carries the lightest background, i.e.
// where the travelling highlight currently sits.
func brightestColumn(t *testing.T, bar string) int {
	t.Helper()
	best, bestSum := -1, -1
	for i, c := range columnColours(t, bar) {
		if sum := c[0] + c[1] + c[2]; sum > bestSum {
			best, bestSum = i, sum
		}
	}
	return best
}

func TestGlowSweepMovesItsHighlight(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	const width = 60
	shine := lipgloss.Color("#ffffff")

	// Once the band is fully on the bar, each frame must move it right.
	prev := -1
	for phase := SweepBand; phase < SweepBand+20; phase++ {
		col := brightestColumn(t, GlowSweep("row", width, glowFG, glowPeak, glowBase, shine, phase))
		if prev >= 0 && col <= prev {
			t.Fatalf("at phase %d the highlight sat at column %d, not right of %d", phase, col, prev)
		}
		prev = col
	}
}

func TestGlowSweepWraps(t *testing.T) {
	// The phase counter only ever grows, so a sweep that did not wrap would
	// leave the bar permanently unlit after the first pass.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	const width = 60
	period := width + 2*SweepBand
	a := GlowSweep("row", width, glowFG, glowPeak, glowBase, lipgloss.Color("#ffffff"), 5)
	b := GlowSweep("row", width, glowFG, glowPeak, glowBase, lipgloss.Color("#ffffff"), 5+period)
	if a != b {
		t.Error("a full period must return the sweep to the same frame")
	}
}

func TestGlowSweepKeepsTheWidthAndText(t *testing.T) {
	for _, phase := range []int{0, 7, 40, 1000} {
		got := GlowSweep("DW - Core", 40, glowFG, glowPeak, glowBase, lipgloss.Color("#ffffff"), phase)
		if lipgloss.Width(got) != 40 {
			t.Errorf("phase %d: bar is %d columns, want 40", phase, lipgloss.Width(got))
		}
		if !strings.Contains(got, "DW - Core") {
			t.Errorf("phase %d: bar dropped its text", phase)
		}
	}
}

func TestGlowSweepFallsBackForNonHexColours(t *testing.T) {
	got := GlowSweep("row", 30, glowFG, lipgloss.Color("8"), glowBase, glowPeak, 3)
	if lipgloss.Width(got) != 30 {
		t.Errorf("fallback bar is %d columns, want 30", lipgloss.Width(got))
	}
}

func TestBandFalloffPeaksAtTheCentre(t *testing.T) {
	if got := bandFalloff(0, 10); got != 1 {
		t.Errorf("falloff at the centre = %v, want 1", got)
	}
	if got := bandFalloff(10, 10); got != 0 {
		t.Errorf("falloff at the edge = %v, want 0", got)
	}
	if got := bandFalloff(-11, 10); got != 0 {
		t.Errorf("falloff beyond the band = %v, want 0", got)
	}
	// Symmetric: the band lights the same either side of its centre.
	if bandFalloff(4, 10) != bandFalloff(-4, 10) {
		t.Error("falloff is not symmetric")
	}
}

func TestAnimationEnabledRespectsTheEnvironment(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	t.Setenv("FUTILS_NO_ANIM", "")
	if !AnimationEnabled() {
		t.Error("animation must be on by default on a colour terminal")
	}
	t.Setenv("FUTILS_NO_ANIM", "1")
	if AnimationEnabled() {
		t.Error("FUTILS_NO_ANIM must switch animation off")
	}
}

func TestAnimationDisabledWithoutColour(t *testing.T) {
	// Ticking a repaint on a terminal that cannot show the difference is pure
	// waste, and it garbles piped output.
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Setenv("FUTILS_NO_ANIM", "")
	if AnimationEnabled() {
		t.Error("animation must stay off when there is no colour to animate")
	}
}

func TestGlowSweepMergesEqualNeighbours(t *testing.T) {
	// A smooth fade across a wide row passes through far fewer distinct 8-bit
	// values than it has columns, so most neighbours are identical. Styling
	// each column separately would send several KB per row per frame; this
	// asserts the merge that avoids it is still happening.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	const width = 158
	bar := GlowSweep("CC - DP - DEV", width, glowFG, glowPeak, glowBase, lipgloss.Color("#8fb2bd"), 7)

	runs := len(runPattern.FindAllString(bar, -1))
	if runs >= width/2 {
		t.Errorf("%d styled runs for %d columns — neighbours are not being merged", runs, width)
	}
	// The merge must not cost columns: every one still has to be painted.
	if got := len(columnColours(t, bar)); got != width {
		t.Errorf("%d columns painted, want %d", got, width)
	}
}

// Benchmarks for the cursor bar. Kept because the cost here is paid on a timer
// when the row animates, so a regression is not something a user would report
// as "slow" — it would show up as a warm laptop.
//
//	go test ./internal/ui/ -bench GlowSweep -benchtime=2000x
func BenchmarkGlowSweep(b *testing.B) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		GlowSweep("CC - DP - DEV", 158, glowFG, glowPeak, glowBase, lipgloss.Color("#8fb2bd"), i)
	}
}

func BenchmarkGlowBar(b *testing.B) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		GlowBar("CC - DP - DEV", 158, glowFG, glowPeak, glowBase)
	}
}
