package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sendKey(m confirmModel, k tea.KeyMsg) confirmModel {
	nm, _ := m.Update(k)
	return nm.(confirmModel)
}

func TestConfirmDefaultsToNo(t *testing.T) {
	// huh focused No by default; preserve that (and it's the safe default for
	// deploy prompts — a stray Enter must not say Yes).
	if newConfirmModel("Proceed?").value {
		t.Error("confirm must default to No (value=false)")
	}
}

func TestConfirmYesKeySubmitsTrue(t *testing.T) {
	m := sendKey(newConfirmModel("?"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if !m.done || !m.value || m.aborted {
		t.Errorf("'y' must submit Yes: done=%v value=%v aborted=%v", m.done, m.value, m.aborted)
	}
}

func TestConfirmNoKeySubmitsFalse(t *testing.T) {
	m := sendKey(newConfirmModel("?"), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !m.done || m.value || m.aborted {
		t.Errorf("'n' must submit No: done=%v value=%v", m.done, m.value)
	}
}

func TestConfirmArrowsSelectButDontSubmit(t *testing.T) {
	yes := sendKey(newConfirmModel("?"), tea.KeyMsg{Type: tea.KeyLeft})
	if !yes.value {
		t.Error("left must select Yes")
	}
	no := sendKey(yes, tea.KeyMsg{Type: tea.KeyRight})
	if no.value {
		t.Error("right must select No")
	}
	if yes.done || no.done {
		t.Error("arrow keys must not submit")
	}
}

func TestConfirmEnterSubmitsCurrentValue(t *testing.T) {
	m := newConfirmModel("?")
	m.value = true
	m = sendKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done || !m.value || m.aborted {
		t.Errorf("enter must submit the current value: %+v", m)
	}
}

func TestConfirmEscAndCtrlCAbort(t *testing.T) {
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		m := sendKey(newConfirmModel("?"), k)
		if !m.aborted || !m.done {
			t.Errorf("%v must abort (Confirm maps this to ErrGoBack): %+v", k, m)
		}
	}
}

func TestConfirmView(t *testing.T) {
	out := newConfirmModel("Map 4 refs now?").View()
	for _, want := range []string{"Map 4 refs now?", "Yes", "No", "submit"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirm view missing %q in:\n%s", want, out)
		}
	}
	ab := newConfirmModel("x")
	ab.aborted, ab.done = true, true
	if ab.View() != "" {
		t.Errorf("aborted confirm must render empty, got %q", ab.View())
	}
}

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

func TestMenuNumbersOnlyFirstNineSelectable(t *testing.T) {
	// The digit shortcut only handles 1-9 (see Update), so rows past the 9th
	// must NOT show a number — "10)"/"11)" are misleading (typing "1" jumps to
	// row 1, never 10). They still render, selectable via arrows, with a bullet.
	var opts []MenuOption
	for _, l := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} { // 11 rows
		opts = append(opts, MenuOption{Label: l, Value: l})
	}
	out := menuModel{message: "Pick", options: opts}.View()

	if !strings.Contains(out, "9)") {
		t.Error("the 9th selectable row must still be numbered 9)")
	}
	if strings.Contains(out, "10)") || strings.Contains(out, "11)") {
		t.Errorf("rows past 9 must not be numbered (misleading), got:\n%s", out)
	}
	if n := strings.Count(out, "·"); n != 2 {
		t.Errorf("expected 2 bullet markers for rows 10-11, got %d:\n%s", n, out)
	}
	// The unnumbered rows must still be present (selectable via arrows).
	if !strings.Contains(out, "j") || !strings.Contains(out, "k") {
		t.Errorf("rows 10-11 (j, k) must still render, got:\n%s", out)
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
