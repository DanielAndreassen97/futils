package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestCursorPointerReservesTheSameGutterEitherWay(t *testing.T) {
	// Different gutter widths would make every list shift sideways as the
	// cursor moves.
	sel, plain := lipgloss.Width(CursorPointer(true)), lipgloss.Width(CursorPointer(false))
	if sel != plain {
		t.Errorf("selected gutter is %d columns, unselected is %d", sel, plain)
	}
	if sel != CursorGutterW {
		t.Errorf("gutter is %d columns but CursorGutterW says %d", sel, CursorGutterW)
	}
}

func TestCursorPointerCarriesTheArrowOnlyWhenSelected(t *testing.T) {
	if !strings.Contains(CursorPointer(true), "❯") {
		t.Error("the selected gutter must carry the arrow")
	}
	if strings.Contains(CursorPointer(false), "❯") {
		t.Error("an unselected row must not carry an arrow")
	}
}

func TestCursorLabelIsUntouchedWhenNotSelected(t *testing.T) {
	if got := CursorLabel("Run notebook", false); got != "Run notebook" {
		t.Errorf("unselected label = %q, want it untouched", got)
	}
}

func TestCursorLabelTakesTheAccentColour(t *testing.T) {
	// Tests run with colour stripped, so force a profile — otherwise this
	// asserts nothing at all.
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	got := CursorLabel("Run notebook", true)
	if !strings.Contains(got, "Run notebook") {
		t.Fatalf("label lost its text: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("selected label = %q, want it styled", got)
	}
	if got == "Run notebook" {
		t.Error("selected label is indistinguishable from an unselected one")
	}
}

func TestDefaultFilterRowRendererUsesTheSharedCursor(t *testing.T) {
	// Every picker that does not supply its own renderer goes through this one,
	// so it is where the shared treatment has to land.
	opt := FilterOption{Label: "DW - DEV - Config", Value: "x"}

	if got := DefaultFilterRowRenderer(opt, true); !strings.HasPrefix(got, "❯ ") {
		t.Errorf("selected row = %q, want the arrow first", got)
	}
	sel := lipgloss.Width(DefaultFilterRowRenderer(opt, true))
	plain := lipgloss.Width(DefaultFilterRowRenderer(opt, false))
	if sel != plain {
		t.Errorf("selected row is %d columns, unselected is %d", sel, plain)
	}
}

func TestDefaultFilterRowRendererLeavesHeadersAlone(t *testing.T) {
	// A section heading is not selectable, so it must not reserve a cursor
	// gutter it can never use.
	got := DefaultFilterRowRenderer(FilterOption{Label: "ADMIN · 34", IsHeader: true}, false)
	if strings.HasPrefix(got, "  ") {
		t.Errorf("header = %q, want no cursor gutter", got)
	}
}
