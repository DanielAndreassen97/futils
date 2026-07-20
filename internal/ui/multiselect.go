package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Shared visual styles for the checkbox widget. Tuned to match the look
// frefresh-go uses for its TableCheckbox: accent-green pointer on the
// cursor row, white-bold label on focused items, accent-green ■ for
// checked items, dim grey for everything else. Package-private so
// callers can't accidentally override them.
var (
	checkboxPointerStyle      = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	checkboxCheckedBoxStyle   = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	checkboxCheckedLabelStyle = lipgloss.NewStyle().Foreground(AccentColor)
	checkboxCursorLabelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)
	checkboxHintStyle         = lipgloss.NewStyle().Foreground(DimColor)
)

// Jump distance for alt+↑/alt+↓ / pgup / pgdown. Five is arbitrary but
// consistent with frefresh-go and works well for ~20-50 item lists.
const checkboxJumpSize = 5

type checkboxItem struct {
	label    string
	checked  bool
	style    lipgloss.Style // when styled, the label's base style (class color)
	styled   bool           // true for MultiSelectRich items; false keeps legacy rendering
	skipBulk bool           // true = excluded from select-all (a); deliberate per-item only
}

type checkboxModel struct {
	title      string
	items      []checkboxItem
	cursor     int
	termHeight int
	done       bool
	goBack     bool
	quit       bool
	goHome     bool
	// filter enables the always-on type-to-filter mode: printable keys build a
	// query, the list narrows to matching rows, and the single-letter shortcuts
	// (a/j/k/g/G/b/q/m) are unavailable — arrows navigate, ctrl+a bulk-toggles.
	// The cursor then addresses a position in the FILTERED view, not items.
	filter bool
	query  string
}

func (m checkboxModel) Init() tea.Cmd { return nil }

// visibleIdx returns the indices of items whose label matches the query
// (case-insensitive substring; empty query = everything), in list order.
func (m checkboxModel) visibleIdx() []int {
	if !m.filter || m.query == "" {
		out := make([]int, len(m.items))
		for i := range m.items {
			out[i] = i
		}
		return out
	}
	q := strings.ToLower(m.query)
	var out []int
	for i, it := range m.items {
		if strings.Contains(strings.ToLower(it.label), q) {
			out = append(out, i)
		}
	}
	return out
}

// updateFiltered is the KeyMsg handler in filter mode. Selections persist
// across query changes — filtering only affects visibility, never checked
// state — so a selection can be built across several searches (same contract
// as the refresh table picker).
func (m checkboxModel) updateFiltered(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	visible := m.visibleIdx()
	clamp := func() {
		if m.cursor >= len(visible) {
			m.cursor = len(visible) - 1
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
	}
	switch msg.String() {
	case "up":
		if len(visible) > 0 {
			m.cursor = (m.cursor - 1 + len(visible)) % len(visible)
		}
	case "down":
		if len(visible) > 0 {
			m.cursor = (m.cursor + 1) % len(visible)
		}
	case "alt+up", "pgup":
		m.cursor -= checkboxJumpSize
		clamp()
	case "alt+down", "pgdown":
		m.cursor += checkboxJumpSize
		clamp()
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = len(visible) - 1
		clamp()
	case " ":
		if m.cursor < len(visible) {
			i := visible[m.cursor]
			m.items[i].checked = !m.items[i].checked
		}
	case "ctrl+a":
		// Toggle the VISIBLE rows only: check all matches, or clear them when
		// they're already all checked. Bulk-excluded rows stay untouched.
		allChecked := true
		for _, i := range visible {
			if !m.items[i].skipBulk && !m.items[i].checked {
				allChecked = false
				break
			}
		}
		for _, i := range visible {
			if !m.items[i].skipBulk {
				m.items[i].checked = !allChecked
			}
		}
	case "enter":
		m.done = true
		return m, tea.Quit
	case "esc":
		if m.query != "" {
			m.query = ""
			m.cursor = 0
			return m, nil
		}
		m.goBack = true
		m.done = true
		return m, tea.Quit
	case "ctrl+c":
		m.quit = true
		m.done = true
		return m, tea.Quit
	case "backspace":
		if m.query != "" {
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.cursor = 0
		}
	default:
		if r := msg.Runes; len(r) == 1 && msg.Type == tea.KeyRunes {
			m.query += string(r)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m checkboxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
	case tea.KeyMsg:
		if m.filter {
			return m.updateFiltered(msg)
		}
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
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.items) - 1
		case " ":
			// Toggle the row under the cursor. Space is the industry-
			// standard checkbox-toggle key in terminal UIs.
			m.items[m.cursor].checked = !m.items[m.cursor].checked
		case "a":
			// Select all if any bulk-selectable row is unchecked; otherwise clear
			// them. Rows flagged skipBulk (destructive deletes) are never touched.
			allChecked := true
			for _, it := range m.items {
				if it.skipBulk {
					continue
				}
				if !it.checked {
					allChecked = false
					break
				}
			}
			for i := range m.items {
				if m.items[i].skipBulk {
					continue
				}
				m.items[i].checked = !allChecked
			}
		case "enter":
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
		case "m":
			m.goHome = true
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m checkboxModel) renderItem(i int) string {
	return m.renderRow(i, i == m.cursor)
}

// renderRow renders one item row; isCursor is passed explicitly because in
// filter mode the cursor addresses a position in the filtered view, so the
// caller decides which item is under it.
func (m checkboxModel) renderRow(i int, isCursor bool) string {
	item := m.items[i]

	pointer := "  "
	if isCursor {
		pointer = checkboxPointerStyle.Render("❯ ")
	}

	box := "□ "
	if item.checked {
		box = checkboxCheckedBoxStyle.Render("■ ")
	}

	var label string
	if item.styled {
		// Colored row (e.g. a class-colored deploy item): the style is the label's
		// base so the color shows whether or not the row is checked — the ■ box is
		// the checked indicator. The cursor row keeps the color, bolded.
		s := item.style
		if isCursor {
			s = s.Bold(true)
		}
		label = s.Render(item.label)
	} else {
		label = item.label
		switch {
		case isCursor:
			label = checkboxCursorLabelStyle.Render(label)
		case item.checked:
			label = checkboxCheckedLabelStyle.Render(label)
		}
	}

	return fmt.Sprintf("%s%s%s", pointer, box, label)
}

func (m checkboxModel) countChecked() int {
	n := 0
	for _, it := range m.items {
		if it.checked {
			n++
		}
	}
	return n
}

// checkedIndices returns the indices of the checked items, in list order.
func (m checkboxModel) checkedIndices() []int {
	var out []int
	for i, it := range m.items {
		if it.checked {
			out = append(out, i)
		}
	}
	return out
}

func (m checkboxModel) View() string {
	// Collapsed post-submit summary — keeps scrollback readable when the
	// user has clicked through multiple screens in sequence.
	if m.done {
		if m.goBack || m.quit || m.goHome {
			return ""
		}
		n := m.countChecked()
		return checkboxCheckedBoxStyle.Render(
			fmt.Sprintf("  %s: %d selected", m.title, n)) + "\n"
	}

	if m.filter {
		return m.viewFiltered()
	}

	var b strings.Builder
	hint := "space toggle • enter confirm • alt+↑↓ jump • a select all • esc back • m main menu • q quit"
	fmt.Fprintf(&b, "  %s\n", m.title)
	fmt.Fprintf(&b, "  %s\n\n", checkboxHintStyle.Render(hint))

	// Header rows above: 3 lines (title, hint, blank).
	windowedList(&b, len(m.items), m.cursor, m.termHeight, 3, checkboxHintStyle, m.renderItem)
	return b.String()
}

// viewFiltered renders the type-to-filter variant: query line under the hint,
// only matching rows, and a live selected-count so selections made under
// earlier queries stay visible even when their rows are filtered out.
func (m checkboxModel) viewFiltered() string {
	var b strings.Builder
	hint := "type to filter • space toggle • ctrl+a select visible • enter confirm • esc clear/back • ↑↓ navigate"
	fmt.Fprintf(&b, "  %s\n", m.title)
	fmt.Fprintf(&b, "  %s\n", checkboxHintStyle.Render(hint))

	query := m.query
	if query == "" {
		query = checkboxHintStyle.Render("filter…")
	}
	fmt.Fprintf(&b, "  %s %s   %s\n\n",
		checkboxPointerStyle.Render("›"), query,
		checkboxHintStyle.Render(fmt.Sprintf("%d selected", m.countChecked())))

	visible := m.visibleIdx()
	if len(visible) == 0 {
		fmt.Fprintf(&b, "  %s\n", checkboxHintStyle.Render("(no matches)"))
		return b.String()
	}

	// Header rows above: 4 lines (title, hint, query, blank).
	windowedList(&b, len(visible), m.cursor, m.termHeight, 4, checkboxHintStyle, func(pos int) string {
		return m.renderRow(visible[pos], pos == m.cursor)
	})
	return b.String()
}

// CheckItem is one row for MultiSelectRich: a display label, an optional lipgloss
// style (zero value renders plain), and whether it starts checked.
type CheckItem struct {
	Label   string
	Style   lipgloss.Style
	Checked bool
	// SkipBulkSelect excludes this row from the select-all (a) key — used for
	// destructive (delete) rows so a bulk select never marks one.
	SkipBulkSelect bool
}

func toCheckboxItems(items []CheckItem) []checkboxItem {
	out := make([]checkboxItem, len(items))
	for i, it := range items {
		out[i] = checkboxItem{label: it.Label, checked: it.Checked, style: it.Style, styled: true, skipBulk: it.SkipBulkSelect}
	}
	return out
}

// MultiSelectRich shows an interactive checkbox list of styled items and returns
// the indices of the checked items in list order. Unlike MultiSelect (which keys
// on the label string), callers map the returned indices back to their own data,
// so two rows with identical labels stay distinct. Returns ErrGoBack on esc,
// ErrQuit on ctrl+c/q.
func MultiSelectRich(title string, items []CheckItem) ([]int, error) {
	model := checkboxModel{title: title, items: toCheckboxItems(items)}
	final, err := tea.NewProgram(model).Run()
	if err != nil {
		return nil, err
	}
	result := final.(checkboxModel)
	if result.quit {
		return nil, ErrQuit
	}
	if result.goHome {
		return nil, ErrGoHome
	}
	if result.goBack {
		return nil, ErrGoBack
	}
	return result.checkedIndices(), nil
}

// MultiSelect shows an interactive checkbox list. Items in `initial` are
// pre-checked — useful for "edit existing favourites" flows where you
// want the user to see and toggle current state.
//
// Returns the checked items in the order they appear in `options`
// (not selection order), so favourites look the same regardless of
// whether the user clicked top-to-bottom or bottom-to-top.
//
// The list has an always-on type-to-filter search (like the refresh table
// picker): printable keys narrow the list, selections persist across query
// changes, and a live selected-count shows what's checked outside the current
// match set. Because typing owns the letter keys, navigation is arrows-only:
//
//	↑/↓            single row (within the filtered view)
//	alt+↑/↓ pgup/pgdown   jump 5 rows
//	home/end       first / last
//	space          toggle cursor row
//	ctrl+a         select all visible (or clear them if already all checked)
//	enter          confirm • esc clear filter, then go back • ctrl+c quit
//
// Returns ErrGoBack on esc, ErrQuit on ctrl+c.
func MultiSelect(title string, options []string, initial []string) ([]string, error) {
	initialSet := make(map[string]bool, len(initial))
	for _, s := range initial {
		initialSet[s] = true
	}
	items := make([]checkboxItem, len(options))
	for i, o := range options {
		items[i] = checkboxItem{label: o, checked: initialSet[o]}
	}

	model := checkboxModel{title: title, items: items, filter: true}
	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	result := final.(checkboxModel)
	if result.quit {
		return nil, ErrQuit
	}
	if result.goHome {
		return nil, ErrGoHome
	}
	if result.goBack {
		return nil, ErrGoBack
	}

	out := make([]string, 0, result.countChecked())
	for _, it := range result.items {
		if it.checked {
			out = append(out, it.label)
		}
	}
	return out, nil
}
