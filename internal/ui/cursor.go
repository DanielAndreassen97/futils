package ui

import "github.com/charmbracelet/lipgloss"

// The cursor treatment is defined once, here, and used by every list in the TUI:
// numbered menus, filter pickers, checkbox lists and the directory browser. They
// drifted into four different treatments — arrow with a plain label, green label
// with no arrow, a background bar, a white label — which reads as four different
// widgets rather than one tool.
//
// Two signals, deliberately: the arrow is a shape and the colour is a colour. A
// terminal without colour keeps the arrow; someone who cannot distinguish the
// green keeps the arrow too. Neither signal alone covers both.
const (
	// cursorArrow is two columns wide, so every row reserves the same gutter and
	// labels line up whether or not they are under the cursor.
	cursorArrow = "❯ "
	cursorBlank = "  "
	// CursorGutterW is what that gutter costs a caller doing its own column
	// arithmetic.
	CursorGutterW = 2
)

// One style, not one per element: the arrow and the label are the same treatment,
// and two identical definitions are two things to keep in sync for no gain.
var cursorStyle = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)

// CursorPointer returns the cursor gutter for a row: the accent arrow when the
// row is under the cursor, blank space of the same width otherwise.
func CursorPointer(selected bool) string {
	if selected {
		return cursorStyle.Render(cursorArrow)
	}
	return cursorBlank
}

// CursorLabel renders a row's text in the cursor treatment when selected, and
// returns it untouched otherwise.
func CursorLabel(s string, selected bool) string {
	if selected {
		return cursorStyle.Render(s)
	}
	return s
}
