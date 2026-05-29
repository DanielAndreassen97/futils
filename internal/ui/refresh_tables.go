package ui

import (
	"fmt"
	"sort"
	"strings"

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
	return refreshTableModel{
		message:   message,
		items:     items,
		groupMap:  groupMap,
		parentMap: parentMap,
		allGroups: allGroups,
		allItems:  allItems,
	}
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

func (m refreshTableModel) Init() tea.Cmd { return nil }

func (m refreshTableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(m.items)
		case "alt+up", "alt+k", "pgup":
			m.cursor -= checkboxJumpSize
			if m.cursor < 0 {
				m.cursor = 0
			}
		case "alt+down", "alt+j", "pgdown":
			m.cursor += checkboxJumpSize
			if m.cursor >= len(m.items) {
				m.cursor = len(m.items) - 1
			}
		case " ":
			m.toggle(m.cursor)
		case "enter":
			m.selection = m.collectSelection()
			m.done = true
			return m, tea.Quit
		case "esc", "b":
			m.goBack = true
			m.done = true
			return m, tea.Quit
		case "ctrl+c", "q":
			m.quit = true
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m refreshTableModel) renderItem(i int) string {
	item := m.items[i]
	locked := m.isLocked(i)
	checked := item.checked || locked
	isCursor := i == m.cursor

	pointer := "  "
	if isCursor {
		pointer = checkboxPointerStyle.Render("❯ ")
	}

	box := "□ "
	if checked {
		box = checkboxCheckedBoxStyle.Render("■ ")
	}

	var label string
	if item.kind == refreshItemAll || item.kind == refreshItemGroup {
		label = fmt.Sprintf("── %s ──", item.label)
	} else {
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
	fmt.Fprintf(&b, "  %s  %s\n\n", m.message, checkboxHintStyle.Render("(space to toggle, alt+↑↓ to jump)"))

	maxVisible := m.termHeight - 3
	if maxVisible <= 0 || maxVisible >= len(m.items) {
		for i := range m.items {
			fmt.Fprintf(&b, "%s\n", m.renderItem(i))
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
	if end > len(m.items) {
		end = len(m.items)
		start = end - itemSlots
		if start < 0 {
			start = 0
		}
	}
	dimStyle := lipgloss.NewStyle().Foreground(DimColor)
	if start > 0 {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(fmt.Sprintf("↑ %d more above", start)))
	}
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%s\n", m.renderItem(i))
	}
	if end < len(m.items) {
		fmt.Fprintf(&b, "  %s\n", dimStyle.Render(fmt.Sprintf("↓ %d more below", len(m.items)-end)))
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
