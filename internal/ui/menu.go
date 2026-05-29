package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// ErrGoBack is returned when the user presses Esc/b to go back one step.
// Callers handle it non-fatally (re-show parent menu) so TUI navigation
// feels like browser back rather than an error state.
var ErrGoBack = errors.New("go back")

// ErrQuit is returned when the user presses Ctrl+C or q to quit outright.
var ErrQuit = errors.New("quit")

// MenuOption is one row in a NumberMenu. Label is what the user sees,
// Value is what gets returned when they select it — decoupled so display
// names can differ from internal identifiers (e.g. notebook display name
// vs. item ID).
//
// IsHeader marks a non-selectable section label. Header rows render in
// dim style with no number/pointer, are skipped by cursor navigation,
// and don't consume a digit shortcut — selectable items are numbered
// independently so "1, 2, 3" stays sane even when headers sit between
// them.
type MenuOption struct {
	Label    string
	Value    string
	IsHeader bool
}

type menuModel struct {
	message  string
	options  []MenuOption
	cursor   int
	selected string
	goBack   bool
	quit     bool
	done     bool
}

func (m menuModel) Init() tea.Cmd { return nil }

// stepCursor moves the cursor by `delta` (±1 typically), skipping over
// IsHeader rows. Wraps around. Headers and any all-header list are no-ops.
func (m *menuModel) stepCursor(delta int) {
	n := len(m.options)
	if n == 0 {
		return
	}
	for i := 0; i < n; i++ {
		m.cursor = (m.cursor + delta + n) % n
		if !m.options[m.cursor].IsHeader {
			return
		}
	}
}

// selectableIndices returns the option indices that are not headers, in
// menu order. The slice position is what the user sees as the digit
// shortcut: selectable[0] → press "1", selectable[1] → press "2", etc.
func (m menuModel) selectableIndices() []int {
	out := make([]int, 0, len(m.options))
	for i, opt := range m.options {
		if !opt.IsHeader {
			out = append(out, i)
		}
	}
	return out
}

func (m menuModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.stepCursor(-1)
		case "down", "j":
			m.stepCursor(1)
		case "enter":
			if len(m.options) == 0 || m.options[m.cursor].IsHeader {
				break
			}
			m.selected = m.options[m.cursor].Value
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
		default:
			// Number keys 1-9 jump straight to the Nth *selectable* row.
			// Saves keystrokes when the menu is short and the user knows
			// which option they want without scrolling.
			if len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '9' {
				idx := int(msg.String()[0] - '1')
				selectable := m.selectableIndices()
				if idx < len(selectable) {
					m.selected = m.options[selectable[idx]].Value
					m.done = true
					return m, tea.Quit
				}
			}
		}
	}
	return m, nil
}

var (
	menuPointerStyle  = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	menuNumberStyle   = lipgloss.NewStyle().Foreground(DimColor)
	menuSelectedStyle = lipgloss.NewStyle().Foreground(AccentColor)
	menuHeaderStyle   = lipgloss.NewStyle().Foreground(DimColor).Bold(true)
)

func (m menuModel) selectedLabel() string {
	for _, opt := range m.options {
		if opt.Value == m.selected {
			return opt.Label
		}
	}
	return ""
}

func (m menuModel) View() string {
	// After selection, collapse to a one-line summary. Keeps scrollback
	// readable when the user has clicked through a multi-step flow.
	if m.done {
		if m.goBack || m.quit {
			return ""
		}
		return menuSelectedStyle.Render(fmt.Sprintf("  %s: %s", m.message, m.selectedLabel())) + "\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n\n", m.message)

	// Number only the selectable rows. Headers print without pointer or
	// digit, with leading blank line for breathing room (unless first row).
	displayNum := 0
	for i, opt := range m.options {
		if opt.IsHeader {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "  %s\n", menuHeaderStyle.Render(opt.Label))
			continue
		}
		displayNum++
		pointer := "  "
		if i == m.cursor {
			pointer = menuPointerStyle.Render("❯ ")
		}
		num := menuNumberStyle.Render(fmt.Sprintf("%d)", displayNum))
		fmt.Fprintf(&b, "%s%s %s\n", pointer, num, opt.Label)
	}

	return b.String()
}

// MenuOptionsFromStrings is a shortcut for menus where the user sees the
// same text they'd pass programmatically (e.g. customer names).
func MenuOptionsFromStrings(values []string) []MenuOption {
	opts := make([]MenuOption, len(values))
	for i, v := range values {
		opts[i] = MenuOption{Label: v, Value: v}
	}
	return opts
}

// NumberMenu shows a single-select menu with arrow-key navigation and
// digit shortcuts. Returns ErrGoBack on esc/b and ErrQuit on ctrl+c/q so
// callers can handle cancellation cleanly.
//
// Options with IsHeader=true render as non-selectable section labels —
// the cursor lands on the first non-header row and skips headers when
// arrow keys move through the list.
func NumberMenu(message string, options []MenuOption) (string, error) {
	// Guard against caller bugs: an all-header (or empty) menu has no
	// selectable rows and would wedge the user with no exit other than
	// esc. Surface it as a programming error rather than a TUI dead-end.
	hasSelectable := false
	for _, opt := range options {
		if !opt.IsHeader {
			hasSelectable = true
			break
		}
	}
	if !hasSelectable {
		return "", fmt.Errorf("NumberMenu %q called with no selectable options", message)
	}

	model := menuModel{message: message, options: options}
	// Start the cursor on the first selectable row so headers at the top
	// don't make Enter immediately no-op.
	for i, opt := range options {
		if !opt.IsHeader {
			model.cursor = i
			break
		}
	}
	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	result := final.(menuModel)
	if result.quit {
		return "", ErrQuit
	}
	if result.goBack {
		return "", ErrGoBack
	}
	return result.selected, nil
}

// Confirm shows a yes/no prompt. Returns true for yes. Themed to the
// accent colour for visual consistency with the rest of the UI.
func Confirm(message string) (bool, error) {
	var result bool
	theme := huh.ThemeBase()
	theme.Focused.FocusedButton = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(AccentColor).Padding(0, 1)
	theme.Focused.BlurredButton = lipgloss.NewStyle().Foreground(DimColor).Padding(0, 1)
	theme.Focused.Title = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)

	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(message).
				Affirmative("Yes").
				Negative("No").
				Value(&result),
		),
	).WithTheme(theme).WithKeyMap(km).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, ErrGoBack
		}
		return false, err
	}
	return result, nil
}
