package ui

import "github.com/charmbracelet/lipgloss"

// AccentColor is the brand green used for cursors, highlights and focus
// states throughout the UI. This is the futils-specific palette —
// frefresh-go uses orange (#e8712a); futils uses green (Tailwind
// green-500) to keep the two tools visually distinct when muscle memory
// would otherwise confuse them.
var AccentColor = lipgloss.Color("#22c55e")

// DimColor is the muted gray used for secondary text (hints, labels,
// deselected rows). Terminal-theme-agnostic — ANSI 8 reads as grey on
// both light and dark backgrounds.
var DimColor = lipgloss.Color("8")

// WarnColor is the yellow used for "pay attention" tags like inline
// badges. ANSI 3 stays within the terminal-adaptive 0-7 range, same
// rationale as DimColor, so it reads as yellow on both light and dark
// backgrounds instead of a fixed hex that could clash with either.
var WarnColor = lipgloss.Color("3")

// ItemTypeColor returns the lipgloss color used to render a Fabric
// item type label in the move picker. Notebooks are accent green
// (matching the brand), Reports are orange (matching the sibling
// tool's accent), Semantic Models stay at the default terminal
// foreground. Unknown types fall back to the default fg.
func ItemTypeColor(itemType string) lipgloss.Color {
	switch itemType {
	case "Notebook":
		return AccentColor
	case "Report":
		return lipgloss.Color("#e8712a")
	case "SemanticModel":
		return lipgloss.Color("")
	}
	return lipgloss.Color("")
}
