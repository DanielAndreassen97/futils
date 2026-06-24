package ui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// barEmptyStyle dims the unfilled cells so the green fill reads as progress.
var barEmptyStyle = lipgloss.NewStyle().Foreground(DimColor)

// RenderBar renders a progress bar of the given cell width: round(frac*width)
// filled cells (▰, brand green) followed by the rest empty (▱, dim). frac is
// clamped to [0,1]. Matches the spinner's ▰▱ block aesthetic. Pure and
// deterministic — the ANSI color codes wrap the runes but don't change the cell
// counts, so callers can count ▰/▱ to assert progress.
func RenderBar(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	if width < 0 {
		width = 0
	}
	filled := int(math.Round(frac * float64(width)))
	if filled > width {
		filled = width
	}
	empty := width - filled
	return spinnerStyle.Render(strings.Repeat("▰", filled)) +
		barEmptyStyle.Render(strings.Repeat("▱", empty))
}
