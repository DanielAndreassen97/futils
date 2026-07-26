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
