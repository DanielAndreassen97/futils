package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newFilterTestModel(labels []string) filterMenuModel {
	ti := textinput.New()
	ti.Focus()
	opts := make([]FilterOption, len(labels))
	for i, l := range labels {
		opts[i] = FilterOption{Label: l, Value: l}
	}
	m := filterMenuModel{
		title:    "test",
		input:    ti,
		options:  opts,
		filtered: make([]int, 0, len(opts)),
		render:   DefaultFilterRowRenderer,
	}
	return m.refilter()
}

func typeRunes(m filterMenuModel, s string) filterMenuModel {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(filterMenuModel)
	}
	return m
}

func TestFilterMenu_EmptyFilterShowsAll(t *testing.T) {
	m := newFilterTestModel([]string{"alpha", "beta", "gamma"})
	if len(m.filtered) != 3 {
		t.Errorf("expected all 3 rows visible, got %d", len(m.filtered))
	}
}

func TestFilterMenu_TypingFiltersBySubstring(t *testing.T) {
	m := newFilterTestModel([]string{"alpha", "beta", "gamma", "delta"})
	m = typeRunes(m, "a")
	// "alpha", "gamma", "delta" all contain 'a'; "beta" also contains 'a'.
	// So all four still match. Stricter:
	m = typeRunes(m, "lp")
	if len(m.filtered) != 1 || m.options[m.filtered[0]].Label != "alpha" {
		t.Errorf("expected only 'alpha' after typing 'alp', got filtered=%v", m.filtered)
	}
}

func TestFilterMenu_FilterIsCaseInsensitive(t *testing.T) {
	m := newFilterTestModel([]string{"Alpha", "BETA"})
	m = typeRunes(m, "BETA")
	if len(m.filtered) != 1 {
		t.Errorf("expected case-insensitive match, got %d rows", len(m.filtered))
	}
	m = newFilterTestModel([]string{"Alpha", "BETA"})
	m = typeRunes(m, "alpha")
	if len(m.filtered) != 1 {
		t.Errorf("expected case-insensitive match for 'alpha', got %d rows", len(m.filtered))
	}
}

func TestFilterMenu_DownAfterFilterStaysInFilteredSubset(t *testing.T) {
	m := newFilterTestModel([]string{"alpha", "beta", "gamma"})
	m = typeRunes(m, "lp") // only "alpha" matches — narrows to a single row
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(filterMenuModel)
	if m.cursor != 0 {
		t.Errorf("cursor should stay at 0 in single-row filter, got %d", m.cursor)
	}
}

func TestFilterMenu_NoMatchClampsCursor(t *testing.T) {
	m := newFilterTestModel([]string{"alpha", "beta"})
	m = typeRunes(m, "zzz")
	if len(m.filtered) != 0 {
		t.Errorf("expected 0 matches for 'zzz', got %d", len(m.filtered))
	}
	// Cursor must not be negative when there are no matches.
	if m.cursor < 0 {
		t.Errorf("cursor went negative: %d", m.cursor)
	}
}

func TestFilterMenu_EscReturnsGoBack(t *testing.T) {
	m := newFilterTestModel([]string{"alpha", "beta"})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(filterMenuModel)
	if !m.goBack || !m.done {
		t.Errorf("expected goBack and done after esc, got goBack=%v done=%v", m.goBack, m.done)
	}
}

func TestFilterMenu_EnterOnEmptyFilterDoesNothing(t *testing.T) {
	m := newFilterTestModel([]string{"alpha"})
	m = typeRunes(m, "zzz")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(filterMenuModel)
	if m.done {
		t.Errorf("Enter on no-match filter must not mark done")
	}
}

// newGroupedTestModel builds a list with section headers. Headers are labels
// prefixed with "#" here purely so the fixture reads clearly.
func newGroupedTestModel(rows []FilterOption) filterMenuModel {
	ti := textinput.New()
	ti.Focus()
	m := filterMenuModel{
		title:    "test",
		input:    ti,
		options:  rows,
		filtered: make([]int, 0, len(rows)),
		render:   DefaultFilterRowRenderer,
	}
	return m.refilter()
}

func groupedFixture() []FilterOption {
	return []FilterOption{
		{Label: "+ Create", Value: "__create"},
		{Label: "ADMIN", IsHeader: true},
		{Label: "alpha", Value: "alpha"},
		{Label: "beta", Value: "beta"},
		{Label: "VIEWER", IsHeader: true},
		{Label: "gamma", Value: "gamma"},
	}
}

// labelAt returns the label the cursor currently sits on.
func labelAt(m filterMenuModel) string {
	if len(m.filtered) == 0 {
		return ""
	}
	return m.options[m.filtered[m.cursor]].Label
}

func TestFilterMenu_CursorStartsOffAHeader(t *testing.T) {
	// The first row here is selectable, but a list that opens with a header
	// must not park the cursor on something Enter can't select.
	m := newGroupedTestModel(groupedFixture()[1:])
	if labelAt(m) != "alpha" {
		t.Errorf("cursor started on %q, want the first selectable row", labelAt(m))
	}
}

func TestFilterMenu_DownSkipsHeaders(t *testing.T) {
	m := newGroupedTestModel(groupedFixture())
	want := []string{"alpha", "beta", "gamma", "+ Create"}
	for _, w := range want {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(filterMenuModel)
		if labelAt(m) != w {
			t.Fatalf("after down, cursor = %q, want %q", labelAt(m), w)
		}
	}
}

func TestFilterMenu_UpSkipsHeaders(t *testing.T) {
	m := newGroupedTestModel(groupedFixture())
	// From "+ Create" upwards wraps to the last selectable row, not the header
	// that sits above it.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(filterMenuModel)
	if labelAt(m) != "gamma" {
		t.Fatalf("after up, cursor = %q, want gamma", labelAt(m))
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(filterMenuModel)
	if labelAt(m) != "beta" {
		t.Fatalf("after second up, cursor = %q, want beta", labelAt(m))
	}
}

func TestFilterMenu_EnterNeverSelectsAHeader(t *testing.T) {
	m := newGroupedTestModel(groupedFixture())
	// Force the cursor onto the ADMIN header, the state a naive clamp could
	// leave behind, and make sure Enter refuses it.
	for i, idx := range m.filtered {
		if m.options[idx].IsHeader {
			m.cursor = i
			break
		}
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(filterMenuModel)
	if m.done {
		t.Error("enter on a header must not complete the picker")
	}
}

func TestFilterMenu_FilterDropsHeadersWithNoMatches(t *testing.T) {
	m := newGroupedTestModel(groupedFixture())
	m = typeRunes(m, "gam")

	var labels []string
	for _, idx := range m.filtered {
		labels = append(labels, m.options[idx].Label)
	}
	want := []string{"VIEWER", "gamma"}
	if len(labels) != len(want) {
		t.Fatalf("visible rows = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("visible rows = %v, want %v", labels, want)
		}
	}
}

func TestFilterMenu_HeadersNeverMatchTheNeedle(t *testing.T) {
	// Typing "admin" must not leave a bare ADMIN header with nothing under it.
	m := newGroupedTestModel(groupedFixture())
	m = typeRunes(m, "admin")
	if len(m.filtered) != 0 {
		var labels []string
		for _, idx := range m.filtered {
			labels = append(labels, m.options[idx].Label)
		}
		t.Errorf("visible rows = %v, want none", labels)
	}
}

func TestFilterMenu_RowsBeforeTheFirstHeaderSurviveFiltering(t *testing.T) {
	m := newGroupedTestModel(groupedFixture())
	m = typeRunes(m, "creat")
	if len(m.filtered) != 1 || m.options[m.filtered[0]].Label != "+ Create" {
		t.Errorf("the ungrouped create row must still match on its own")
	}
}
