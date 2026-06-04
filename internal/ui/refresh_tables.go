package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Categorizer maps a table name to a group label used by TableCheckbox's
// cascade picker. Callers supply this so internal/ui stays free of any
// customer-specific naming conventions (e.g. Norwegian "Fakta", German
// "Faktentabelle"). A nil categorizer means "no grouping" — every table
// lives under a single bucket.
type Categorizer func(tableName string) string

// allTablesGroup is the bucket name used when no categorizer is supplied.
// One group, one toggle — degenerate but always works.
const allTablesGroup = "Tables"

type refreshItemKind int

const (
	refreshItemAll refreshItemKind = iota
	refreshItemGroup
	refreshItemTable
)

type refreshCheckItem struct {
	kind      refreshItemKind
	label     string
	tableName string
	group     string
	checked   bool
}

type refreshTableModel struct {
	message    string
	items      []refreshCheckItem
	input      textinput.Model
	filtered   []int // indices into items, after filtering; cursor indexes THIS
	cursor     int
	selection  TableSelection
	goBack     bool
	quit       bool
	done       bool
	groupMap   map[int][]int // group index -> child indices
	parentMap  map[int]int   // child index -> group index
	allGroups  []int
	allItems   []int
	termHeight int
}

func buildRefreshItems(tables []string, categorizer Categorizer) ([]refreshCheckItem, map[int][]int, map[int]int, []int, []int) {
	// Group tables by the caller-supplied categorizer. Order is the
	// first-seen order so a customer-specific Dim/Fact/Log/Other
	// convention stays visually predictable across runs.
	groups := map[string][]string{}
	var groupOrder []string
	for _, t := range tables {
		var g string
		if categorizer != nil {
			g = categorizer(t)
		} else {
			g = allTablesGroup
		}
		if _, seen := groups[g]; !seen {
			groupOrder = append(groupOrder, g)
		}
		groups[g] = append(groups[g], t)
	}

	var items []refreshCheckItem
	groupMap := map[int][]int{}
	parentMap := map[int]int{}
	var allGroups, allItems []int

	items = append(items, refreshCheckItem{
		kind:  refreshItemAll,
		label: fmt.Sprintf("All tables (%d)", len(tables)),
	})

	for _, gName := range groupOrder {
		gTables := groups[gName]
		if len(gTables) == 0 {
			continue
		}
		sort.Strings(gTables)
		gIdx := len(items)
		items = append(items, refreshCheckItem{
			kind:  refreshItemGroup,
			label: fmt.Sprintf("All %s (%d)", gName, len(gTables)),
			group: gName,
		})
		allGroups = append(allGroups, gIdx)

		var children []int
		for _, t := range gTables {
			cIdx := len(items)
			items = append(items, refreshCheckItem{
				kind:      refreshItemTable,
				label:     t,
				tableName: t,
				group:     gName,
			})
			children = append(children, cIdx)
			allItems = append(allItems, cIdx)
			parentMap[cIdx] = gIdx
		}
		groupMap[gIdx] = children
	}
	return items, groupMap, parentMap, allGroups, allItems
}

func newRefreshTableModel(message string, tables []string, categorizer Categorizer) refreshTableModel {
	items, groupMap, parentMap, allGroups, allItems := buildRefreshItems(tables, categorizer)
	ti := textinput.New()
	ti.Placeholder = "filter…"
	ti.Focus()
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(AccentColor)
	m := refreshTableModel{
		message:   message,
		items:     items,
		input:     ti,
		filtered:  make([]int, 0, len(items)),
		groupMap:  groupMap,
		parentMap: parentMap,
		allGroups: allGroups,
		allItems:  allItems,
	}
	return m.refilter()
}

// inFiltered reports whether the items-index idx is currently visible.
func (m refreshTableModel) inFiltered(idx int) bool {
	for _, fi := range m.filtered {
		if fi == idx {
			return true
		}
	}
	return false
}

// visibleChildren returns the children of a group header that survive the
// current filter. Used so a filtered group-toggle only touches what's shown.
func (m refreshTableModel) visibleChildren(groupIdx int) []int {
	var out []int
	for _, ci := range m.groupMap[groupIdx] {
		if m.inFiltered(ci) {
			out = append(out, ci)
		}
	}
	return out
}

// refilter recomputes m.filtered from the search input. An empty needle
// shows the full hierarchy (degenerate to today's behavior). A non-empty
// needle keeps table rows whose name contains it, plus the group headers
// that still have a matching child; the global "All tables" row is hidden
// because "all matches" isn't the same as a full-model refresh.
func (m refreshTableModel) refilter() refreshTableModel {
	needle := strings.ToLower(strings.TrimSpace(m.input.Value()))
	m.filtered = m.filtered[:0]
	if needle == "" {
		for i := range m.items {
			m.filtered = append(m.filtered, i)
		}
	} else {
		matchGroup := map[int]bool{}
		for i, it := range m.items {
			if it.kind == refreshItemTable && strings.Contains(strings.ToLower(it.tableName), needle) {
				matchGroup[m.parentMap[i]] = true
			}
		}
		for i, it := range m.items {
			switch it.kind {
			case refreshItemAll:
				// hidden while filtering
			case refreshItemGroup:
				if matchGroup[i] {
					m.filtered = append(m.filtered, i)
				}
			case refreshItemTable:
				if strings.Contains(strings.ToLower(it.tableName), needle) {
					m.filtered = append(m.filtered, i)
				}
			}
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

// isLocked returns true for items the user can't toggle directly because
// a parent group ("All" / "All Dim") is already checked. The whole point
// is so the cascade is one-way visible — when "All tables" is on, you
// can see every child as ✓-but-greyed instead of a single ambiguous row.
func (m refreshTableModel) isLocked(idx int) bool {
	item := m.items[idx]
	if item.kind == refreshItemAll {
		return false
	}
	if m.items[0].checked {
		return true
	}
	if item.kind == refreshItemTable {
		return m.items[m.parentMap[idx]].checked
	}
	return false
}

func (m *refreshTableModel) toggle(idx int) {
	if m.isLocked(idx) {
		return
	}
	item := &m.items[idx]
	item.checked = !item.checked

	if item.kind == refreshItemAll {
		for _, gi := range m.allGroups {
			m.items[gi].checked = item.checked
		}
		for _, ti := range m.allItems {
			m.items[ti].checked = item.checked
		}
	} else if item.kind == refreshItemGroup {
		for _, ci := range m.groupMap[idx] {
			m.items[ci].checked = item.checked
		}
	}
}

// toggleAt toggles the row at the given position within m.filtered. With an
// empty filter it defers to the normal cascade in toggle(). With an active
// filter, a group header instead bulk-toggles only its *visible* matches and
// leaves the persistent group.checked cascade flag alone — that flag means
// "all children", which would be wrong when only a subset is shown.
func (m *refreshTableModel) toggleAt(pos int) {
	if pos < 0 || pos >= len(m.filtered) {
		return
	}
	idx := m.filtered[pos]
	filtering := strings.TrimSpace(m.input.Value()) != ""

	if filtering && m.items[idx].kind == refreshItemGroup {
		visible := m.visibleChildren(idx)
		allChecked := len(visible) > 0
		for _, ci := range visible {
			if !m.items[ci].checked {
				allChecked = false
				break
			}
		}
		for _, ci := range visible {
			if m.isLocked(ci) {
				continue
			}
			m.items[ci].checked = !allChecked
		}
		return
	}
	m.toggle(idx)
}

// TableSelection is what TableCheckbox returns. FullRefresh=true means
// "no objects in the refresh body" (i.e. refresh the entire model). Otherwise
// Tables is the explicit list passed to the Enhanced Refresh API.
type TableSelection struct {
	FullRefresh bool
	Tables      []string
	Summary     string
}

func (m refreshTableModel) collectSelection() TableSelection {
	if m.items[0].checked {
		return TableSelection{FullRefresh: true, Summary: "Full model refresh"}
	}
	var tables []string
	var summaryParts []string
	for _, gIdx := range m.allGroups {
		group := m.items[gIdx]
		children := m.groupMap[gIdx]
		if group.checked {
			for _, ci := range children {
				tables = append(tables, m.items[ci].tableName)
			}
			summaryParts = append(summaryParts, fmt.Sprintf("All %s (%d)", group.group, len(children)))
		} else {
			for _, ci := range children {
				if m.items[ci].checked {
					tables = append(tables, m.items[ci].tableName)
					summaryParts = append(summaryParts, m.items[ci].tableName)
				}
			}
		}
	}
	sort.Strings(tables)
	return TableSelection{Tables: tables, Summary: strings.Join(summaryParts, ", ")}
}

func (m refreshTableModel) Init() tea.Cmd { return textinput.Blink }

func (m refreshTableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
		return m, nil
	case tea.KeyMsg:
		// The always-on filter owns printable keys, so navigation is on the
		// arrow/page keys only — letter shortcuts (j/k/b/q) would be typed
		// into the search box instead.
		switch msg.String() {
		case "up":
			if len(m.filtered) > 0 {
				m.cursor = (m.cursor - 1 + len(m.filtered)) % len(m.filtered)
			}
			return m, nil
		case "down":
			if len(m.filtered) > 0 {
				m.cursor = (m.cursor + 1) % len(m.filtered)
			}
			return m, nil
		case "alt+up", "pgup":
			m.cursor -= checkboxJumpSize
			if m.cursor < 0 {
				m.cursor = 0
			}
			return m, nil
		case "alt+down", "pgdown":
			m.cursor += checkboxJumpSize
			if m.cursor >= len(m.filtered) {
				m.cursor = len(m.filtered) - 1
			}
			if m.cursor < 0 {
				m.cursor = 0
			}
			return m, nil
		case " ":
			m.toggleAt(m.cursor)
			return m, nil
		case "enter":
			m.selection = m.collectSelection()
			m.done = true
			return m, tea.Quit
		case "esc":
			m.goBack = true
			m.done = true
			return m, tea.Quit
		case "ctrl+c":
			m.quit = true
			m.done = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m = m.refilter()
	return m, cmd
}

func (m refreshTableModel) renderItem(idx int, isCursor bool) string {
	item := m.items[idx]
	locked := m.isLocked(idx)
	checked := item.checked || locked
	filtering := strings.TrimSpace(m.input.Value()) != ""

	// While filtering, a group header carries no cascade flag — its box
	// reflects whether every *visible* match below it is checked, and its
	// label shows the match count instead of the group total.
	groupLabel := item.label
	if filtering && item.kind == refreshItemGroup {
		vis := m.visibleChildren(idx)
		allChecked := len(vis) > 0
		for _, ci := range vis {
			if !m.items[ci].checked && !m.isLocked(ci) {
				allChecked = false
				break
			}
		}
		checked = allChecked
		groupLabel = fmt.Sprintf("All %s (%d matches)", item.group, len(vis))
	}

	pointer := "  "
	if isCursor {
		pointer = checkboxPointerStyle.Render("❯ ")
	}

	box := "□ "
	if checked {
		box = checkboxCheckedBoxStyle.Render("■ ")
	}

	var label string
	switch item.kind {
	case refreshItemAll:
		label = fmt.Sprintf("── %s ──", item.label)
	case refreshItemGroup:
		label = fmt.Sprintf("── %s ──", groupLabel)
	default:
		label = fmt.Sprintf("    %s", item.label)
	}

	dimStyle := lipgloss.NewStyle().Foreground(DimColor)
	if locked {
		box = dimStyle.Render("■ ")
		label = dimStyle.Render(label)
	} else if isCursor {
		label = checkboxCursorLabelStyle.Render(label)
	} else if checked {
		label = checkboxCheckedLabelStyle.Render(label)
	}

	return fmt.Sprintf("%s%s%s", pointer, box, label)
}

func (m refreshTableModel) View() string {
	if m.done {
		if m.goBack || m.quit {
			return ""
		}
		return checkboxCheckedLabelStyle.Render(fmt.Sprintf("  Tables: %s", m.selection.Summary)) + "\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n", m.message)
	fmt.Fprintf(&b, "  %s\n", m.input.View())
	fmt.Fprintf(&b, "  %s\n\n", checkboxHintStyle.Render("type to filter • space toggle • ↑↓ navigate • enter confirm • esc back"))

	dimStyle := lipgloss.NewStyle().Foreground(DimColor)
	if len(m.filtered) == 0 {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render("(no matches)"))
		return b.String()
	}

	// Header rows above the list: message, input, hint, blank.
	maxVisible := m.termHeight - 5
	if maxVisible <= 0 || maxVisible >= len(m.filtered) {
		for i, idx := range m.filtered {
			fmt.Fprintf(&b, "%s\n", m.renderItem(idx, i == m.cursor))
		}
		return b.String()
	}

	itemSlots := maxVisible - 2
	if itemSlots < 1 {
		itemSlots = 1
	}
	start := m.cursor - itemSlots/2
	if start < 0 {
		start = 0
	}
	end := start + itemSlots
	if end > len(m.filtered) {
		end = len(m.filtered)
		start = end - itemSlots
		if start < 0 {
			start = 0
		}
	}
	if start > 0 {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(fmt.Sprintf("↑ %d more above", start)))
	}
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%s\n", m.renderItem(m.filtered[i], i == m.cursor))
	}
	if end < len(m.filtered) {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(fmt.Sprintf("↓ %d more below", len(m.filtered)-end)))
	}
	return b.String()
}

// TableCheckbox shows the refresh-specific table picker. Returns a
// TableSelection describing what to refresh, or ErrGoBack/ErrQuit if the
// user backed out.
//
// categorizer may be nil — in that case every table goes into a single
// "Tables" bucket and the cascade only has the global All toggle. Pass
// a customer-specific categorizer (e.g. Dim/Fakta/Log) from the cmd
// layer to get domain-aware grouping without coupling internal/ui to
// any one customer's naming convention.
func TableCheckbox(message string, tables []string, categorizer Categorizer) (TableSelection, error) {
	model := newRefreshTableModel(message, tables, categorizer)
	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		return TableSelection{}, err
	}
	result := final.(refreshTableModel)
	if result.quit {
		return TableSelection{}, ErrQuit
	}
	if result.goBack {
		return TableSelection{}, ErrGoBack
	}
	return result.selection, nil
}
