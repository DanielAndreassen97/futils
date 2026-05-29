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
