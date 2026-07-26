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
// The fade is static, not animated: a pulsing row would redraw the whole list
// on a timer for a decoration, and bubbletea pickers here only repaint on input.
//
// Colours that are not "#rrggbb" fall back to a flat peak-coloured bar, so an
// ANSI palette entry degrades instead of rendering garbage.
func GlowBar(s string, width int, fg, peak, base lipgloss.Color) string {
	if width <= 0 {
		return ""
	}
	content := []rune(FitWidth(s, width))

	pr, pg, pb, okPeak := hexRGB(peak)
	br, bg, bb, okBase := hexRGB(base)
	if !okPeak || !okBase {
		return lipgloss.NewStyle().Background(peak).Foreground(fg).Bold(true).
			Width(width).Render(s)
	}

	var out strings.Builder
	for i, r := range content {
		t := 0.0
		if width > 1 {
			t = float64(i) / float64(width-1)
		}
		cell := lipgloss.Color(rgbHex(
			lerpInt(pr, br, t),
			lerpInt(pg, bg, t),
			lerpInt(pb, bb, t),
		))
		out.WriteString(lipgloss.NewStyle().
			Background(cell).Foreground(fg).Bold(true).
			Render(string(r)))
	}
	return out.String()
}

// SweepBand is how many columns the travelling highlight covers. Wide enough to
// read as light rather than a moving cursor block.
const SweepBand = 14

// GlowSweep is GlowBar with a soft specular band travelling across the row.
// phase is a frame counter the caller advances; the band position derives from
// it and wraps, so callers need no state beyond an int.
//
// Terminals cannot fade a background within a cell, so the band is built the
// only way available: every column is rendered as its own interpolated colour.
// That is one Render per column per frame, which is why the caller controls the
// frame rate.
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

	content := []rune(FitWidth(s, width))
	// The band starts fully off the left edge and finishes fully off the right,
	// so it enters and leaves instead of popping in at the margins.
	period := width + 2*SweepBand
	pos := phase%period - SweepBand

	var out strings.Builder
	for i, r := range content {
		t := 0.0
		if width > 1 {
			t = float64(i) / float64(width-1)
		}
		cr := lerpInt(pr, br, t)
		cg := lerpInt(pg, bg, t)
		cb := lerpInt(pb, bb, t)

		if k := bandFalloff(i-pos, SweepBand); k > 0 {
			cr = lerpInt(cr, sr, k)
			cg = lerpInt(cg, sg, k)
			cb = lerpInt(cb, sb, k)
		}

		out.WriteString(lipgloss.NewStyle().
			Background(lipgloss.Color(rgbHex(cr, cg, cb))).
			Foreground(fg).Bold(true).
			Render(string(r)))
	}
	return out.String()
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
