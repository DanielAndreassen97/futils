package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newRefreshTestModel builds a refreshTableModel via the real constructor
// with a Dim/Fact categorizer, mirroring cmd.categorizeRefreshTable.
func newRefreshTestModel(tables []string) refreshTableModel {
	cat := func(name string) string {
		if strings.HasPrefix(name, "Dim") {
			return "Dim"
		}
		return "Fact"
	}
	return newRefreshTableModel("test", tables, cat)
}

func typeRefresh(m refreshTableModel, s string) refreshTableModel {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(refreshTableModel)
	}
	return m
}

func backspaceRefresh(m refreshTableModel, n int) refreshTableModel {
	for i := 0; i < n; i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(refreshTableModel)
	}
	return m
}

func refreshSpace(m refreshTableModel) refreshTableModel {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	return next.(refreshTableModel)
}

func refreshDown(m refreshTableModel) refreshTableModel {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	return next.(refreshTableModel)
}

// itemIndexByTable returns the items index of the row for tableName, or -1.
func itemIndexByTable(m refreshTableModel, tableName string) int {
	for i, it := range m.items {
		if it.kind == refreshItemTable && it.tableName == tableName {
			return i
		}
	}
	return -1
}

// groupIndexFor returns the items index of the group header for group, or -1.
func groupIndexFor(m refreshTableModel, group string) int {
	for i, it := range m.items {
		if it.kind == refreshItemGroup && it.group == group {
			return i
		}
	}
	return -1
}

// filteredHas reports whether items-index idx is currently visible.
func filteredHas(m refreshTableModel, idx int) bool {
	for _, fi := range m.filtered {
		if fi == idx {
			return true
		}
	}
	return false
}

// moveCursorTo points the cursor at the filtered row backing items-index idx.
func moveCursorTo(m refreshTableModel, idx int) refreshTableModel {
	for i, fi := range m.filtered {
		if fi == idx {
			m.cursor = i
			return m
		}
	}
	return m
}

func TestRefreshFilter_EmptyShowsAllItems(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Org", "Dim Ansatt", "Fakta Lonn"})
	if len(m.filtered) != len(m.items) {
		t.Errorf("empty filter should show all %d items, got %d", len(m.items), len(m.filtered))
	}
}

func TestRefreshFilter_TypingShowsMatchesAndGroupHeaders(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim Ansatt", "Fakta Lonn"})
	m = typeRefresh(m, "org")

	orgIdx := itemIndexByTable(m, "Dim Organisasjon")
	if !filteredHas(m, orgIdx) {
		t.Errorf("Dim Organisasjon should be visible after typing 'org'")
	}
	if filteredHas(m, itemIndexByTable(m, "Dim Ansatt")) {
		t.Errorf("Dim Ansatt should be hidden after typing 'org'")
	}
	// The global "All tables" header (items[0]) is hidden while filtering.
	if filteredHas(m, 0) {
		t.Errorf("All-tables header should be hidden while filter active")
	}
	// The Dim group header stays (it has a matching child).
	if !filteredHas(m, groupIndexFor(m, "Dim")) {
		t.Errorf("Dim group header should be visible when a Dim table matches")
	}
	// The Fact group header is hidden (no matching child).
	if filteredHas(m, groupIndexFor(m, "Fact")) {
		t.Errorf("Fact group header should be hidden when no Fact table matches")
	}
}

func TestRefreshFilter_CaseInsensitive(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim Ansatt"})
	m = typeRefresh(m, "ORG")
	if !filteredHas(m, itemIndexByTable(m, "Dim Organisasjon")) {
		t.Errorf("filter should be case-insensitive")
	}
}

func TestRefreshFilter_ToggleSurvivesFilterClear(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim Ansatt"})
	m = typeRefresh(m, "org")
	orgIdx := itemIndexByTable(m, "Dim Organisasjon")
	m = moveCursorTo(m, orgIdx)
	m = refreshSpace(m)
	if !m.items[orgIdx].checked {
		t.Fatalf("Dim Organisasjon should be checked after space")
	}

	m = backspaceRefresh(m, 3)
	if m.input.Value() != "" {
		t.Fatalf("filter not cleared, value=%q", m.input.Value())
	}
	if !m.items[orgIdx].checked {
		t.Errorf("toggle must survive filter clear")
	}
	if len(m.filtered) != len(m.items) {
		t.Errorf("clearing filter should restore all %d items, got %d", len(m.items), len(m.filtered))
	}
}

func TestRefreshFilter_GroupToggleAffectsOnlyVisibleMatches(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim OrgEnhet", "Dim Ansatt"})
	m = typeRefresh(m, "org")

	groupIdx := groupIndexFor(m, "Dim")
	m = moveCursorTo(m, groupIdx)
	m = refreshSpace(m)

	orgIdx := itemIndexByTable(m, "Dim Organisasjon")
	orgEnhetIdx := itemIndexByTable(m, "Dim OrgEnhet")
	ansattIdx := itemIndexByTable(m, "Dim Ansatt")

	if !m.items[orgIdx].checked || !m.items[orgEnhetIdx].checked {
		t.Errorf("group toggle should check both visible matches")
	}
	if m.items[ansattIdx].checked {
		t.Errorf("group toggle must NOT check the hidden Dim Ansatt")
	}
	// The persistent cascade flag must stay off — it means "all 47".
	if m.items[groupIdx].checked {
		t.Errorf("filtered group toggle must not set the cascade group.checked flag")
	}
}

func TestRefreshFilter_CollectSelectionAfterFilteredToggle(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim Ansatt", "Fakta Lonn"})
	m = typeRefresh(m, "org")
	orgIdx := itemIndexByTable(m, "Dim Organisasjon")
	m = moveCursorTo(m, orgIdx)
	m = refreshSpace(m)

	sel := m.collectSelection()
	if sel.FullRefresh {
		t.Errorf("should not be a full refresh")
	}
	if len(sel.Tables) != 1 || sel.Tables[0] != "Dim Organisasjon" {
		t.Errorf("expected [Dim Organisasjon], got %v", sel.Tables)
	}
}

func TestRefreshFilter_DownStaysInFilteredSubset(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim Organisasjon", "Dim Ansatt", "Fakta Lonn"})
	m = typeRefresh(m, "org")
	// Visible: Dim group header + Dim Organisasjon = 2 rows.
	if len(m.filtered) != 2 {
		t.Fatalf("expected 2 visible rows, got %d", len(m.filtered))
	}
	for i := 0; i < 5; i++ {
		m = refreshDown(m)
		if m.cursor < 0 || m.cursor >= len(m.filtered) {
			t.Fatalf("cursor out of filtered bounds: %d (len %d)", m.cursor, len(m.filtered))
		}
	}
}

func TestRefreshFilter_EmptyFilterGroupCascadeUnchanged(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim A", "Dim B", "Fakta C"})
	groupIdx := groupIndexFor(m, "Dim")
	m = moveCursorTo(m, groupIdx)
	m = refreshSpace(m)

	if !m.items[groupIdx].checked {
		t.Errorf("empty-filter group toggle should set the cascade group.checked flag")
	}
	for _, ci := range m.groupMap[groupIdx] {
		if !m.items[ci].checked {
			t.Errorf("empty-filter group toggle should cascade-check all children")
		}
	}
}

func TestRefreshFilter_NoMatchClampsCursor(t *testing.T) {
	m := newRefreshTestModel([]string{"Dim A", "Dim B"})
	m = typeRefresh(m, "zzz")
	if len(m.filtered) != 0 {
		t.Errorf("expected 0 matches for 'zzz', got %d", len(m.filtered))
	}
	if m.cursor < 0 {
		t.Errorf("cursor must not go negative on no-match, got %d", m.cursor)
	}
}
