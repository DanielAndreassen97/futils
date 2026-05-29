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

func (m filterMenuModel) refilter() filterMenuModel {
	needle := strings.ToLower(strings.TrimSpace(m.input.Value()))
	m.filtered = m.filtered[:0]
	for i, opt := range m.options {
		if needle == "" || strings.Contains(strings.ToLower(opt.Label), needle) {
			m.filtered = append(m.filtered, i)
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

func (m filterMenuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termH = msg.Height
		return m, nil
	case tea.KeyMsg:
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
		case "enter":
			if len(m.filtered) == 0 {
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

	// Viewport: leave ~6 lines for title, input, hint, blanks; clip
	// the visible window around the cursor for long lists.
	headerRows := 5
	maxVisible := m.termH - headerRows - 1
	if maxVisible <= 0 || maxVisible >= len(m.filtered) {
		for i, idx := range m.filtered {
			fmt.Fprintf(&b, "  %s\n", m.render(m.options[idx], i == m.cursor))
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
		fmt.Fprintf(&b, "  %s\n",
			filterMenuHintStyle.Render(fmt.Sprintf("↑ %d more above", start)))
	}
	for i := start; i < end; i++ {
		idx := m.filtered[i]
		fmt.Fprintf(&b, "  %s\n", m.render(m.options[idx], i == m.cursor))
	}
	if end < len(m.filtered) {
		fmt.Fprintf(&b, "  %s\n",
			filterMenuHintStyle.Render(fmt.Sprintf("↓ %d more below", len(m.filtered)-end)))
	}
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
