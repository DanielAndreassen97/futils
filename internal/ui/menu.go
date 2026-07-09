package ui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	Label       string
	Value       string
	IsHeader    bool
	Description string // one-line footer hint shown when this option is highlighted
	Info        string // fuller text shown in a box when the user presses ?
	Badge       string // short inline tag, e.g. "MUST SET" (rendered accent/warn)
}

type menuModel struct {
	message  string
	options  []MenuOption
	cursor   int
	selected string
	goBack   bool
	quit     bool
	done     bool
	showInfo bool
	width    int // terminal width from tea.WindowSizeMsg; 0 until first message
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
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.stepCursor(-1)
			m.showInfo = false
		case "down", "j":
			m.stepCursor(1)
			m.showInfo = false
		case "?":
			m.showInfo = !m.showInfo
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
	menuBadgeStyle    = lipgloss.NewStyle().Foreground(WarnColor)
	menuInfoBoxStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(DimColor).Padding(0, 1)
	// The highlighted option's one-line Description renders in accent so it
	// reads as content about the current row, not another dim key-hint.
	menuDescStyle = lipgloss.NewStyle().Foreground(AccentColor).Italic(true)
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
		// Only 1-9 are digit-jumpable (Update handles a single keypress), so
		// number just those; past the 9th, show a bullet instead of a fake "10)"
		// number. The bullet is padded to the width of "N)" so labels stay aligned.
		marker := " ·"
		if displayNum <= 9 {
			marker = fmt.Sprintf("%d)", displayNum)
		}
		label := opt.Label
		if opt.Badge != "" {
			label += " " + menuBadgeStyle.Render("["+opt.Badge+"]")
		}
		fmt.Fprintf(&b, "%s%s %s\n", pointer, menuNumberStyle.Render(marker), label)
	}

	var cur MenuOption
	if m.cursor >= 0 && m.cursor < len(m.options) {
		cur = m.options[m.cursor]
	}

	// The help footer only renders for menus that actually use the inline-help
	// fields — a plain Label/Value menu (every legacy call site) is left exactly
	// as before, with no footer. Within a help-carrying menu: the nav hint, a
	// "? info" advertisement when the highlighted option has fuller Info, the
	// cursor option's one-line Description, and (on ?) the Info box.
	anyRich := false
	for _, o := range m.options {
		if o.Description != "" || o.Info != "" || o.Badge != "" {
			anyRich = true
			break
		}
	}
	if anyRich {
		hint := "↑/↓ move · enter select · esc back"
		if cur.Info != "" {
			hint += " · ? info"
		}
		b.WriteString("\n  " + confirmHelpStyle.Render(hint) + "\n")

		if cur.Description != "" {
			// Wrap the description to the terminal width instead of running off the
			// edge; PaddingLeft keeps the 2-space indent on wrapped lines. Width is
			// the content area (subtract the 2-space indent). Unknown/tiny width → no wrap.
			desc := menuDescStyle.PaddingLeft(2)
			if m.width > 4 {
				desc = desc.Width(m.width - 2)
			}
			b.WriteString(desc.Render(cur.Description) + "\n")
		}

		if m.showInfo && cur.Info != "" {
			// Wrap the info text to the terminal width. Width() is the content area;
			// the box's border (2) + horizontal padding (2) sit outside it, so
			// subtract 4. Guard a not-yet-known/too-narrow terminal (→ no wrap).
			box := menuInfoBoxStyle
			if m.width > 6 {
				box = box.Width(m.width - 4)
			}
			b.WriteString("\n" + box.Render(cur.Info) + "\n")
		}
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

var (
	confirmTitleStyle    = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	confirmFocusedButton = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(AccentColor).Padding(0, 1)
	confirmBlurredButton = lipgloss.NewStyle().Foreground(DimColor).Padding(0, 1)
	confirmSelectedStyle = lipgloss.NewStyle().Foreground(AccentColor)
	confirmHelpStyle     = lipgloss.NewStyle().Foreground(DimColor)
)

// confirmModel is a yes/no prompt rendered with plain bubbletea on stdout —
// the same stack as menuModel. It deliberately does NOT use huh: huh renders to
// stderr and opens /dev/tty directly to issue cursor-position/background-colour
// queries, and its inline renderer ghosts (stacks duplicate frames) when the
// form runs in a short terminal after a screenful of stdout output. Owning the
// model keeps the rendering consistent and ghost-free.
type confirmModel struct {
	message string
	value   bool // current selection; true = Yes. Defaults to No (safe).
	aborted bool // esc / ctrl+c
	done    bool
}

func newConfirmModel(message string) confirmModel {
	return confirmModel{message: message}
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "left", "h":
			m.value = true // Yes sits on the left
		case "right", "l":
			m.value = false
		case "tab", "shift+tab":
			m.value = !m.value
		case "y", "Y":
			m.value, m.done = true, true
			return m, tea.Quit
		case "n", "N":
			m.value, m.done = false, true
			return m, tea.Quit
		case "enter":
			m.done = true
			return m, tea.Quit
		case "esc", "ctrl+c":
			m.aborted, m.done = true, true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() string {
	if m.done {
		if m.aborted {
			return ""
		}
		ans := "No"
		if m.value {
			ans = "Yes"
		}
		return confirmSelectedStyle.Render(fmt.Sprintf("  %s %s", m.message, ans)) + "\n"
	}

	yes, no := confirmBlurredButton.Render("Yes"), confirmBlurredButton.Render("No")
	if m.value {
		yes = confirmFocusedButton.Render("Yes")
	} else {
		no = confirmFocusedButton.Render("No")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %s\n\n", confirmTitleStyle.Render(m.message))
	fmt.Fprintf(&b, "  %s  %s\n\n", yes, no)
	b.WriteString("  " + confirmHelpStyle.Render("←/→ toggle · enter submit · y Yes · n No"))
	return b.String()
}

// Confirm shows a yes/no prompt and returns true for yes. Esc/Ctrl+C return
// ErrGoBack so callers treat a cancel as "go back", matching the menus.
func Confirm(message string) (bool, error) {
	final, err := tea.NewProgram(newConfirmModel(message)).Run()
	if err != nil {
		return false, err
	}
	res := final.(confirmModel)
	if res.aborted {
		return false, ErrGoBack
	}
	return res.value, nil
}
