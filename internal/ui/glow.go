package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// GlowBar renders s across exactly width columns with a background that fades
// from peak on the left to base on the right, so the row reads as lit rather
// than merely filled. Used for the cursor row in long pickers, where a flat
// highlight is easy to lose among other coloured rows.
//
// Colours that are not "#rrggbb" fall back to a flat peak-coloured bar, so an
// ANSI palette entry degrades instead of rendering garbage.
func GlowBar(s string, width int, fg, peak, base lipgloss.Color) string {
	if width <= 0 {
		return ""
	}
	pr, pg, pb, okPeak := hexRGB(peak)
	br, bg, bb, okBase := hexRGB(base)
	if !okPeak || !okBase {
		return lipgloss.NewStyle().Background(peak).Foreground(fg).Bold(true).
			Width(width).Render(s)
	}
	return paintRuns(FitWidth(s, width), fg, func(i int) (int, int, int) {
		t := fadeAt(i, width)
		return lerpInt(pr, br, t), lerpInt(pg, bg, t), lerpInt(pb, bb, t)
	})
}

// SweepBand is how many columns the travelling highlight covers. Wide enough to
// read as light rather than a moving cursor block.
const SweepBand = 14

// GlowSweep is GlowBar with a soft specular band travelling across the row.
// phase is a frame counter the caller advances; the band position derives from
// it and wraps, so callers need no state beyond an int.
//
// The fade is static, not animated, unless the caller advances phase: a pulsing
// row would redraw the whole list on a timer, and pickers here otherwise only
// repaint on input.
func GlowSweep(s string, width int, fg, peak, base, shine lipgloss.Color, phase int) string {
	if width <= 0 {
		return ""
	}
	pr, pg, pb, okPeak := hexRGB(peak)
	br, bg, bb, okBase := hexRGB(base)
	sr, sg, sb, okShine := hexRGB(shine)
	if !okPeak || !okBase || !okShine {
		return GlowBar(s, width, fg, peak, base)
	}

	// The band starts fully off the left edge and finishes fully off the right,
	// so it enters and leaves instead of popping in at the margins.
	period := width + 2*SweepBand
	pos := phase%period - SweepBand

	return paintRuns(FitWidth(s, width), fg, func(i int) (int, int, int) {
		t := fadeAt(i, width)
		r, g, b := lerpInt(pr, br, t), lerpInt(pg, bg, t), lerpInt(pb, bb, t)
		if k := bandFalloff(i-pos, SweepBand); k > 0 {
			r, g, b = lerpInt(r, sr, k), lerpInt(g, sg, k), lerpInt(b, sb, k)
		}
		return r, g, b
	})
}

// colourStep is how coarsely gradient colours are snapped before being drawn.
// It is what makes neighbouring columns collapse into shared runs: without it a
// fade's three channels drift at different rates, so a run breaks as soon as
// any one of them moves, and a 158-column bar becomes ~110 separate styled runs
// even though it shows only a few dozen perceptibly different colours. Each run
// costs a lipgloss Render and an escape sequence on the wire, so the snapping is
// most of the saving — merging alone recovers barely a third of it.
//
// 4 (64 levels per channel) leaves no flat stretch longer than about eight
// columns, so the fade still reads as smooth. Raising it to 8 is roughly twice
// as cheap again but stretches flat runs past fifteen columns, which starts to
// band visibly in a wide terminal. The cheaper setting is not worth it: even at
// 4 a frame costs a fraction of a millisecond.
const colourStep = 4

func snap(v int) int {
	v = (v + colourStep/2) / colourStep * colourStep
	if v > 255 {
		return 255
	}
	return v
}

// paintRuns writes content with a per-column background from colourAt, snapping
// each colour and merging consecutive columns that end up identical into a
// single styled run.
//
// A terminal cannot fade a background inside a cell, so the gradient has to be
// built column by column. Emitting a separate escape sequence for every column
// would send several KB down the wire for one row, every frame — over ssh that
// is the difference between smooth and laggy.
func paintRuns(content string, fg lipgloss.Color, colourAt func(i int) (r, g, b int)) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return ""
	}
	snapped := func(i int) (int, int, int) {
		r, g, b := colourAt(i)
		return snap(r), snap(g), snap(b)
	}
	style := lipgloss.NewStyle().Foreground(fg).Bold(true)

	var out strings.Builder
	runStart := 0
	pr, pg, pb := snapped(0)

	flush := func(end int) {
		out.WriteString(style.Background(lipgloss.Color(rgbHex(pr, pg, pb))).
			Render(string(runes[runStart:end])))
	}
	for i := 1; i < len(runes); i++ {
		r, g, b := snapped(i)
		if r == pr && g == pg && b == pb {
			continue
		}
		flush(i)
		runStart, pr, pg, pb = i, r, g, b
	}
	flush(len(runes))
	return out.String()
}

// fadeAt is a column's normalised position along the bar, 0 at the left edge
// and 1 at the right.
func fadeAt(i, width int) float64 {
	if width <= 1 {
		return 0
	}
	return float64(i) / float64(width-1)
}

// bandFalloff is the highlight's intensity at distance d from its centre:
// 0 outside the band, rising to 1 at the centre. Squared so the edges taper
// instead of ending in a visible seam.
func bandFalloff(d, band int) float64 {
	if d < 0 {
		d = -d
	}
	if d >= band {
		return 0
	}
	k := 1 - float64(d)/float64(band)
	return k * k
}

// hexRGB parses "#rrggbb" into 8-bit components. ok is false for any other
// form — named ANSI colours ("3", "8") are valid lipgloss colours but carry no
// RGB to interpolate between.
func hexRGB(c lipgloss.Color) (r, g, b int, ok bool) {
	s := string(c)
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
}

func rgbHex(r, g, b int) string { return fmt.Sprintf("#%02x%02x%02x", r, g, b) }

func lerpInt(from, to int, t float64) int {
	return from + int(float64(to-from)*t+0.5)
}
