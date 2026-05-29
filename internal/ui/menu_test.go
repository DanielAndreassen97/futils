package ui

import (
	"strings"
	"testing"
)

func TestMenuHeader_SkipsHeaderInCursorNavigation(t *testing.T) {
	opts := []MenuOption{
		{Label: "Actions", IsHeader: true},
		{Label: "Run", Value: "run"},
		{Label: "Refresh", Value: "refresh"},
		{Label: "Settings", IsHeader: true},
		{Label: "Logout", Value: "logout"},
	}
	m := menuModel{options: opts, cursor: 1} // start on "Run"

	m.stepCursor(1) // → "Refresh"
	if m.cursor != 2 {
		t.Errorf("expected cursor=2 after step, got %d", m.cursor)
	}
	m.stepCursor(1) // should skip "Settings" header → "Logout"
	if m.cursor != 4 {
		t.Errorf("expected cursor to skip header to 4 (Logout), got %d", m.cursor)
	}
	m.stepCursor(1) // wrap, skip "Actions" header → "Run"
	if m.cursor != 1 {
		t.Errorf("expected cursor to wrap and skip first header to 1 (Run), got %d", m.cursor)
	}
	m.stepCursor(-1) // wrap backwards, skip "Settings" header → "Logout"
	if m.cursor != 4 {
		t.Errorf("expected backward wrap to 4 (Logout), got %d", m.cursor)
	}
}

func TestMenuHeader_SelectableIndicesExcludesHeaders(t *testing.T) {
	m := menuModel{options: []MenuOption{
		{Label: "Actions", IsHeader: true},
		{Label: "Run", Value: "run"},
		{Label: "Settings", IsHeader: true},
		{Label: "Logout", Value: "logout"},
	}}
	got := m.selectableIndices()
	want := []int{1, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("selectableIndices = %v, want %v", got, want)
	}
}

func TestMenuHeader_ViewRendersHeaderWithoutNumber(t *testing.T) {
	m := menuModel{
		message: "Pick one",
		options: []MenuOption{
			{Label: "Actions", IsHeader: true},
			{Label: "Run", Value: "run"},
			{Label: "Refresh", Value: "refresh"},
		},
		cursor: 1,
	}
	out := m.View()
	// Header should appear, but its label should NOT be preceded by "1)" —
	// the first selectable item "Run" gets that number instead.
	if !strings.Contains(out, "Actions") {
		t.Error("expected header 'Actions' in output")
	}
	if !strings.Contains(out, "1) Run") && !strings.Contains(out, "1)") {
		t.Errorf("expected first selectable 'Run' to be numbered 1, got:\n%s", out)
	}
	// "Actions" should not be numbered.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Actions") && strings.Contains(line, ")") {
			t.Errorf("header line should not contain ')': %q", line)
		}
	}
}
