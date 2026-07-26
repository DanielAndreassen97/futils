package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FilterOption is one row in a FilterMenu. Label is what the user
// sees and what the filter matches. Value is returned on selection.
// Meta is arbitrary per-row data the caller's renderer can use
// (e.g. the Fabric item type for color-coding).
type FilterOption struct {
	Label string
	Value string
	Meta  any
	// IsHeader marks a section title: rendered like any other row but never
	// selectable, skipped by the cursor, and hidden when filtering leaves its
	// group empty. A header is never matched by the filter itself — matching it
	// would leave a section title standing over nothing.
	IsHeader bool
}

// FilterRowRenderer turns a FilterOption + selection state into a
// rendered string. Selection state takes precedence: a renderer
// MUST return a uniformly-highlighted row when selected, regardless
// of any per-row coloring it would otherwise apply.
type FilterRowRenderer func(opt FilterOption, selected bool) string

// DefaultFilterRowRenderer renders the Label only, with the cursor
// row highlighted in the accent color. Used when callers don't need
// custom per-row styling.
func DefaultFilterRowRenderer(opt FilterOption, selected bool) string {
	if opt.IsHeader {
		return lipgloss.NewStyle().Foreground(DimColor).Bold(true).Render(opt.Label)
	}
	if selected {
		return lipgloss.NewStyle().Foreground(AccentColor).Bold(true).Render(opt.Label)
	}
	return opt.Label
}

// FitWidth sizes s to exactly width display columns: padded with trailing
// spaces when shorter, or truncated with a trailing … when longer. Rune-aware
// (counts runes, not bytes) so accented names still line up in a column.
func FitWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	switch {
	case len(r) == width:
		return s
	case len(r) < width:
		return s + strings.Repeat(" ", width-len(r))
	case width == 1:
		return "…"
	default:
		return string(r[:width-1]) + "…"
	}
}

type filterMenuModel struct {
	title    string
	input    textinput.Model
	options  []FilterOption // full unfiltered list
	filtered []int          // indices into options, after filtering
	cursor   int            // position within filtered
	render   FilterRowRenderer
	termH    int
	done     bool
	goBack   bool
	quit     bool
	selected int // index into options (not filtered) once done
}

var (
	filterMenuTitleStyle = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	filterMenuHintStyle  = lipgloss.NewStyle().Foreground(DimColor)
)

func (m filterMenuModel) Init() tea.Cmd { return textinput.Blink }

// refilter rebuilds the visible row set. A section header is emitted lazily —
// only once the first matching row under it is found — so filtering can never
// leave a title standing over an empty section.
func (m filterMenuModel) refilter() filterMenuModel {
	needle := strings.ToLower(strings.TrimSpace(m.input.Value()))
	m.filtered = m.filtered[:0]

	pendingHeader := -1
	for i, opt := range m.options {
		if opt.IsHeader {
			pendingHeader = i
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(opt.Label), needle) {
			continue
		}
		if pendingHeader >= 0 {
			m.filtered = append(m.filtered, pendingHeader)
			pendingHeader = -1
		}
		m.filtered = append(m.filtered, i)
	}
	return m.settleCursor()
}

// settleCursor clamps the cursor into range and walks it off a header, so the
// row under the cursor is always one Enter can act on.
func (m filterMenuModel) settleCursor() filterMenuModel {
	if len(m.filtered) == 0 {
		m.cursor = 0
		return m
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.options[m.filtered[m.cursor]].IsHeader {
		return m.step(1)
	}
	return m
}

// step moves the cursor by delta with wrap-around, skipping headers. The bound
// makes a list of nothing but headers terminate instead of spinning.
func (m filterMenuModel) step(delta int) filterMenuModel {
	n := len(m.filtered)
	if n == 0 {
		return m
	}
	for i := 0; i < n; i++ {
		m.cursor = (m.cursor + delta + n) % n
		if !m.options[m.filtered[m.cursor]].IsHeader {
			return m
		}
	}
	return m
}

func (m filterMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termH = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			return m.step(-1), nil
		case "down":
			return m.step(1), nil
		case "enter":
			if len(m.filtered) == 0 {
				return m, nil
			}
			if m.options[m.filtered[m.cursor]].IsHeader {
				return m, nil
			}
			m.selected = m.filtered[m.cursor]
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

func (m filterMenuModel) View() string {
	if m.done {
		if m.goBack || m.quit {
			return ""
		}
		sel := m.options[m.selected]
		return filterMenuTitleStyle.Render(
			fmt.Sprintf("  %s: %s", m.title, sel.Label)) + "\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n", filterMenuTitleStyle.Render(m.title))
	fmt.Fprintf(&b, "  %s\n", m.input.View())
	fmt.Fprintf(&b, "  %s\n\n",
		filterMenuHintStyle.Render("type to filter • ↑↓ navigate • enter select • esc back"))

	if len(m.filtered) == 0 {
		fmt.Fprintf(&b, "  %s\n", filterMenuHintStyle.Render("(no matches)"))
		return b.String()
	}

	// Viewport: 5 header rows (title, input, hint, blanks); clip the
	// visible window around the cursor for long lists.
	windowedList(&b, len(m.filtered), m.cursor, m.termH, 5, filterMenuHintStyle, func(pos int) string {
		return "  " + m.render(m.options[m.filtered[pos]], pos == m.cursor)
	})
	return b.String()
}

// FilterMenu shows a searchable single-select list. Typing filters
// the visible rows by case-insensitive substring match on Label;
// arrow keys navigate the filtered subset; Enter selects.
//
// `render` controls per-row appearance and is responsible for
// honoring the `selected` flag — a custom renderer that ignores
// selection state will produce an invisible cursor. Pass
// DefaultFilterRowRenderer when no custom styling is needed.
//
// Returns the chosen option's Value, or ErrGoBack on esc, ErrQuit
// on ctrl+c.
func FilterMenu(title string, options []FilterOption, render FilterRowRenderer) (string, error) {
	if render == nil {
		render = DefaultFilterRowRenderer
	}
	ti := textinput.New()
	ti.Placeholder = "filter…"
	ti.Focus()
	ti.Prompt = "› "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(AccentColor)

	model := filterMenuModel{
		title:    title,
		input:    ti,
		options:  options,
		filtered: make([]int, 0, len(options)),
		render:   render,
	}
	model = model.refilter()

	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	result := final.(filterMenuModel)
	if result.quit {
		return "", ErrQuit
	}
	if result.goBack {
		return "", ErrGoBack
	}
	return result.options[result.selected].Value, nil
}
