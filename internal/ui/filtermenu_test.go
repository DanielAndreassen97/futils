package ui

import (
	"strings"
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

func pinnedFixture() []FilterOption {
	return []FilterOption{
		{Label: "Rename workspace", Value: "__rename", Pinned: true},
		{Label: "Delete workspace", Value: "__delete", Pinned: true},
		{Label: "NOTEBOOK", IsHeader: true},
		{Label: "nb_sales", Value: "nb1"},
		{Label: "nb_orders", Value: "nb2"},
		{Label: "LAKEHOUSE", IsHeader: true},
		{Label: "lh_bronze", Value: "lh1"},
	}
}

// intoList walks the cursor down until it leaves the pinned rows, which is where
// typing starts filtering. Counting arrow presses by hand is fragile — headers
// are skipped, so the number of downs needed depends on the fixture.
func intoList(m filterMenuModel) filterMenuModel {
	for i := 0; i < len(m.options)+1 && m.onPinnedRow(); i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(filterMenuModel)
	}
	return m
}

// ontoPinned walks the cursor up until it reaches a pinned row.
func ontoPinned(m filterMenuModel) filterMenuModel {
	for i := 0; i < len(m.options)+1 && !m.onPinnedRow(); i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = next.(filterMenuModel)
	}
	return m
}

func visibleLabels(m filterMenuModel) []string {
	var out []string
	for _, idx := range m.filtered {
		out = append(out, m.options[idx].Label)
	}
	return out
}

func TestFilterMenu_PinnedRowsSurviveFiltering(t *testing.T) {
	// The workspace actions sit above the item list on the same screen. Typing
	// narrows the items; losing Rename because it does not match "sales" would
	// mean clearing the filter just to reach it.
	m := typeRunes(intoList(newGroupedTestModel(pinnedFixture())), "sales")

	want := []string{"Rename workspace", "Delete workspace", "NOTEBOOK", "nb_sales"}
	got := visibleLabels(m)
	if len(got) != len(want) {
		t.Fatalf("visible = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible = %v, want %v", got, want)
		}
	}
}

func TestFilterMenu_PinnedRowsAreNotMatchedByTheFilter(t *testing.T) {
	// "workspace" appears in both pinned labels. They show because they are
	// pinned, not because they matched — so the items must still be filtered
	// out rather than the whole list surviving.
	m := typeRunes(intoList(newGroupedTestModel(pinnedFixture())), "workspace")

	for _, label := range visibleLabels(m) {
		if label == "nb_sales" || label == "lh_bronze" {
			t.Errorf("item %q survived a filter it does not match", label)
		}
	}
}

func TestFilterMenu_FilterInputHiddenWhileOnAPinnedRow(t *testing.T) {
	// A filter box above rows it cannot filter is a lie about what typing does.
	m := newGroupedTestModel(pinnedFixture())
	if m.filterVisible() {
		t.Error("the filter must be hidden while the cursor sits on a pinned row")
	}
	if !strings.Contains(m.View(), "browse") {
		t.Errorf("the hint should point down to the list:\n%s", m.View())
	}

	if !intoList(m).filterVisible() {
		t.Error("the filter must appear once the cursor reaches the list")
	}
}

func TestFilterMenu_FilterInputShowsWhenTextIsTyped(t *testing.T) {
	// Once text has been typed the box has to stay on screen even if the cursor
	// walks back up to a pinned row, or the user cannot see what is filtering.
	m := typeRunes(intoList(newGroupedTestModel(pinnedFixture())), "sal")
	if !m.filterVisible() {
		t.Fatal("the filter must be visible while the cursor is in the list")
	}
	if !ontoPinned(m).filterVisible() {
		t.Error("the filter must stay visible while it holds text")
	}
}

func TestFilterMenu_PinnedRowIsSelectable(t *testing.T) {
	m := newGroupedTestModel(pinnedFixture())
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res := next.(filterMenuModel)
	if !res.done {
		t.Fatal("enter on a pinned row must select it")
	}
	if res.options[res.selected].Value != "__rename" {
		t.Errorf("selected %q, want __rename", res.options[res.selected].Value)
	}
}

// noItemsFixture is a workspace with nothing in it: pinned actions and no list.
func noItemsFixture() []FilterOption {
	return []FilterOption{
		{Label: "1) Rename workspace", Value: "__rename", Pinned: true},
		{Label: "2) Edit description", Value: "__desc", Pinned: true},
		{Label: "3) Delete workspace", Value: "__delete", Pinned: true},
		{Label: "4) Back", Value: "__back", Pinned: true},
	}
}

func TestFilterMenu_DigitJumpsToAPinnedRow(t *testing.T) {
	// The pinned rows look like a numbered menu, so they have to answer to
	// digits like one. Losing that was the whole complaint about an empty
	// workspace: the screen became a menu you could not use as a menu.
	m := newGroupedTestModel(noItemsFixture())
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	res := next.(filterMenuModel)

	if !res.done {
		t.Fatal("a digit must select the matching pinned row")
	}
	if res.options[res.selected].Value != "__delete" {
		t.Errorf("selected %q, want __delete", res.options[res.selected].Value)
	}
}

func TestFilterMenu_DigitBeyondThePinnedRowsIsIgnored(t *testing.T) {
	m := newGroupedTestModel(noItemsFixture())
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	res := next.(filterMenuModel)
	if res.done {
		t.Error("a digit with no matching row must do nothing")
	}
}

func TestFilterMenu_TypingOnAPinnedRowDoesNotFilter(t *testing.T) {
	// Filtering belongs to the list. While the cursor is up among the actions,
	// letters must not quietly start narrowing something off-screen.
	m := typeRunes(newGroupedTestModel(pinnedFixture()), "sales")

	if got := m.input.Value(); got != "" {
		t.Errorf("filter holds %q — typing on a pinned row must not filter", got)
	}
	if len(m.filtered) != len(pinnedFixture())-2 {
		// Two headers are only emitted when their group has a visible row; with
		// no filter every row shows, so the count is everything but nothing.
		t.Logf("visible = %v", visibleLabels(m))
	}
	for _, want := range []string{"nb_sales", "nb_orders", "lh_bronze"} {
		found := false
		for _, l := range visibleLabels(m) {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q disappeared — nothing should have been filtered", want)
		}
	}
}

func TestFilterMenu_TypingInTheListFilters(t *testing.T) {
	m := typeRunes(intoList(newGroupedTestModel(pinnedFixture())), "sales")

	if m.input.Value() != "sales" {
		t.Fatalf("filter holds %q, want sales", m.input.Value())
	}
	for _, l := range visibleLabels(m) {
		if l == "lh_bronze" {
			t.Error("lh_bronze survived a filter it does not match")
		}
	}
}

func TestFilterMenu_BackspaceClearsTheFilterFromAnywhere(t *testing.T) {
	// Otherwise a user who filters, arrows up to an action, and changes their
	// mind is stuck with a filter they cannot clear.
	m := typeRunes(intoList(newGroupedTestModel(pinnedFixture())), "sales")

	m = ontoPinned(m)
	if !m.onPinnedRow() {
		t.Fatalf("expected to be on a pinned row, got %q", m.options[m.filtered[m.cursor]].Label)
	}
	for i := 0; i < len("sales"); i++ {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(filterMenuModel)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("filter still holds %q after backspacing it away", got)
	}
}

func TestFilterMenu_HintOmitsTheListWhenThereIsNothingToBrowse(t *testing.T) {
	// An empty workspace has no list, so promising one is a lie.
	view := newGroupedTestModel(noItemsFixture()).View()
	if strings.Contains(view, "browse") {
		t.Errorf("hint offers to browse a list that does not exist:\n%s", view)
	}
	if !strings.Contains(view, "1-9") {
		t.Errorf("hint should mention the digit jump:\n%s", view)
	}
}

func TestFilterMenu_HintMentionsTheListWhenThereIsOne(t *testing.T) {
	view := newGroupedTestModel(pinnedFixture()).View()
	if !strings.Contains(view, "browse") {
		t.Errorf("hint should point down to the list:\n%s", view)
	}
}

func TestFilterMenu_SpaceOnAPinnedRowDoesNotFilter(t *testing.T) {
	// Space arrives as its own key type in some bubbletea versions, so it needs
	// swallowing explicitly. A stray space starting a filter from the action
	// rows is the same bug as a letter doing it.
	m := newGroupedTestModel(pinnedFixture())
	for _, k := range []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeyRunes, Runes: []rune(" ")}} {
		next, _ := m.Update(k)
		m = next.(filterMenuModel)
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("filter holds %q after spaces on a pinned row", got)
	}
}
