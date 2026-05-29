package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel mirrors what MultiSelect does internally, so tests can
// drive the same model code paths without spinning up a real program.
func newTestModel(options []string, initial []string) checkboxModel {
	set := make(map[string]bool, len(initial))
	for _, s := range initial {
		set[s] = true
	}
	items := make([]checkboxItem, len(options))
	for i, o := range options {
		items[i] = checkboxItem{label: o, checked: set[o]}
	}
	return checkboxModel{title: "test", items: items}
}

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune(s)})
}

func pressSpace(m checkboxModel) checkboxModel {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	return next.(checkboxModel)
}

func pressDown(m checkboxModel) checkboxModel {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	return next.(checkboxModel)
}

func TestMultiSelect_SpaceTogglesCursorRow(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c"}, nil)
	m = pressSpace(m)
	if !m.items[0].checked {
		t.Errorf("expected items[0] checked after space on cursor=0")
	}
	if m.items[1].checked || m.items[2].checked {
		t.Errorf("space must only toggle the cursor row")
	}
}

func TestMultiSelect_DownMovesCursor(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c"}, nil)
	m = pressDown(m)
	if m.cursor != 1 {
		t.Errorf("expected cursor=1, got %d", m.cursor)
	}
}

func TestMultiSelect_DownWrapsAtEnd(t *testing.T) {
	m := newTestModel([]string{"a", "b"}, nil)
	m = pressDown(m)
	m = pressDown(m)
	if m.cursor != 0 {
		t.Errorf("expected cursor to wrap to 0, got %d", m.cursor)
	}
}

func TestMultiSelect_InitialValuesPreChecked(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c"}, []string{"b"})
	if m.items[1].checked != true {
		t.Errorf("expected items[1] pre-checked")
	}
	if m.items[0].checked || m.items[2].checked {
		t.Errorf("only matching items should be pre-checked")
	}
}

func TestMultiSelect_AToggleAllOnThenOff(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c"}, nil)
	// First `a` should check all.
	next, _ := m.Update(keyMsg("a"))
	m = next.(checkboxModel)
	for i, it := range m.items {
		if !it.checked {
			t.Errorf("expected items[%d] checked after first `a`", i)
		}
	}
	// Second `a` should clear all (since all are checked).
	next, _ = m.Update(keyMsg("a"))
	m = next.(checkboxModel)
	for i, it := range m.items {
		if it.checked {
			t.Errorf("expected items[%d] unchecked after second `a`", i)
		}
	}
}

func TestMultiSelect_JumpRespectsBounds(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}, nil)
	// Jump by 5 (alt+down).
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("j")})
	m = next.(checkboxModel)
	if m.cursor != 5 {
		t.Errorf("expected cursor=5 after alt+j, got %d", m.cursor)
	}
	// Another jump should clamp to last index.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune("j")})
	m = next.(checkboxModel)
	if m.cursor != 9 {
		t.Errorf("expected cursor=9 (last) after second alt+j, got %d", m.cursor)
	}
}

func TestMultiSelect_CountCheckedAccurate(t *testing.T) {
	m := newTestModel([]string{"a", "b", "c"}, []string{"a", "c"})
	if got := m.countChecked(); got != 2 {
		t.Errorf("expected 2 checked, got %d", got)
	}
}
