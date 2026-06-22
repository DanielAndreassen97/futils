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
	label   string
	checked bool
	style   lipgloss.Style // when styled, the label's base style (class color)
	styled  bool           // true for MultiSelectRich items; false keeps legacy rendering
}

type checkboxModel struct {
	title      string
	items      []checkboxItem
	cursor     int
	termHeight int
	done       bool
	goBack     bool
	quit       bool
}

func (m checkboxModel) Init() tea.Cmd { return nil }

func (m checkboxModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.items) - 1
		case " ":
			// Toggle the row under the cursor. Space is the industry-
			// standard checkbox-toggle key in terminal UIs.
			m.items[m.cursor].checked = !m.items[m.cursor].checked
		case "a":
			// Select all if anything is unchecked; otherwise uncheck all.
			// Gives a one-keystroke "clear everything" when the list is
			// already fully checked.
			allChecked := true
			for _, it := range m.items {
				if !it.checked {
					allChecked = false
					break
				}
			}
			for i := range m.items {
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
		}
	}
	return m, nil
}

func (m checkboxModel) renderItem(i int) string {
	item := m.items[i]
	isCursor := i == m.cursor

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
		if m.goBack || m.quit {
			return ""
		}
		n := m.countChecked()
		return checkboxCheckedBoxStyle.Render(
			fmt.Sprintf("  %s: %d selected", m.title, n)) + "\n"
	}

	var b strings.Builder
	hint := "space toggle • enter confirm • alt+↑↓ jump • a select all • esc back"
	fmt.Fprintf(&b, "  %s\n", m.title)
	fmt.Fprintf(&b, "  %s\n\n", checkboxHintStyle.Render(hint))

	// Header rows above: 3 lines (title, hint, blank). Reserve one for
	// the footer hint line in huh-style if we want, but we don't need
	// one — the top hint is enough.
	headerRows := 3
	maxVisible := m.termHeight - headerRows - 1
	if maxVisible <= 0 || maxVisible >= len(m.items) {
		for i := range m.items {
			fmt.Fprintf(&b, "%s\n", m.renderItem(i))
		}
		return b.String()
	}

	// Reserve 2 lines for the "↑ N more above" / "↓ N more below" hints
	// so they never get clipped at the viewport edge.
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

	if start > 0 {
		fmt.Fprintf(&b, "  %s\n",
			checkboxHintStyle.Render(fmt.Sprintf("↑ %d more above", start)))
	}
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%s\n", m.renderItem(i))
	}
	if end < len(m.items) {
		fmt.Fprintf(&b, "  %s\n",
			checkboxHintStyle.Render(fmt.Sprintf("↓ %d more below", len(m.items)-end)))
	}

	return b.String()
}

// CheckItem is one row for MultiSelectRich: a display label, an optional lipgloss
// style (zero value renders plain), and whether it starts checked.
type CheckItem struct {
	Label   string
	Style   lipgloss.Style
	Checked bool
}

func toCheckboxItems(items []CheckItem) []checkboxItem {
	out := make([]checkboxItem, len(items))
	for i, it := range items {
		out[i] = checkboxItem{label: it.Label, checked: it.Checked, style: it.Style, styled: true}
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
// Navigation:
//
//	↑/↓ k/j        single row
//	alt+↑/↓ pgup/pgdown / alt+k/j   jump 5 rows
//	home/g • end/G jump to first / last
//	space          toggle cursor row
//	a              select all (or clear if already full)
//	enter          confirm • esc/b go back • ctrl+c/q quit
//
// Returns ErrGoBack on esc, ErrQuit on ctrl+c/q.
func MultiSelect(title string, options []string, initial []string) ([]string, error) {
	initialSet := make(map[string]bool, len(initial))
	for _, s := range initial {
		initialSet[s] = true
	}
	items := make([]checkboxItem, len(options))
	for i, o := range options {
		items[i] = checkboxItem{label: o, checked: initialSet[o]}
	}

	model := checkboxModel{title: title, items: items}
	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	result := final.(checkboxModel)
	if result.quit {
		return nil, ErrQuit
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
