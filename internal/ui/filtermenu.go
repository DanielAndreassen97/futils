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
	// Badge is a short tag rendered after the label, e.g. "NEEDS ADMIN". Same
	// concept as MenuOption.Badge, so the two widgets render it the same way
	// instead of each caller inventing its own.
	Badge string
	// Pinned turns a row into part of a fixed action section above the filterable
	// list. It carries more than stickiness, and all of it is deliberate:
	//
	//   - the row is always visible, whatever the filter says
	//   - the filter never matches it, so typing a word that appears in a pinned
	//     label narrows the list rather than preserving all of it
	//   - while the cursor is ON a pinned row the section behaves like a numbered
	//     menu: 1-9 selects the Nth pinned row, and letters are ignored rather
	//     than filtering a list the user is not looking at
	//   - the filter input is hidden in that state, since it could not filter
	//     anything on screen
	//
	// FilterMenu moves pinned rows to the front, so their order in the input
	// slice sets their numbers but their position among other rows does not
	// matter.
	Pinned bool
	// pinnedNum is the 1-based digit that selects this pinned row. Assigned by
	// FilterMenu, never by a caller: the number shown and the key that acts on
	// it have to come from the same place or they drift.
	pinnedNum int
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
	return CursorPointer(selected) + CursorLabel(RowLabel(opt), selected) + RowBadge(opt)
}

// RowLabel is a row's label with its pinned-section number prefixed, so the
// digit shown is by construction the digit that selects it. Custom renderers
// should use this rather than numbering labels themselves — hand-written numbers
// drift from the widget's positional dispatch the moment an action is reordered.
func RowLabel(opt FilterOption) string {
	if opt.pinnedNum > 0 {
		return fmt.Sprintf("%d) %s", opt.pinnedNum, opt.Label)
	}
	return opt.Label
}

// RowBadge renders a row's badge, or the empty string when it has none.
func RowBadge(opt FilterOption) string {
	if opt.Badge == "" {
		return ""
	}
	return "  " + filterMenuBadgeStyle.Render("["+opt.Badge+"]")
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
	filterMenuBadgeStyle = lipgloss.NewStyle().Foreground(WarnColor)
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
		if opt.Pinned {
			// Always visible, and never counted as a match — otherwise typing a
			// word that happens to appear in a pinned label would keep the whole
			// list rather than narrowing it.
			m.filtered = append(m.filtered, i)
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

// frontLoadPinned moves pinned rows ahead of everything else, preserving their
// relative order, and numbers them. Enforced rather than documented: a pinned row
// left after a header would render above that header and still answer to digit 1
// from the middle of the list.
func frontLoadPinned(options []FilterOption) []FilterOption {
	pinned := make([]FilterOption, 0, len(options))
	rest := make([]FilterOption, 0, len(options))
	for _, opt := range options {
		if opt.Pinned {
			opt.pinnedNum = len(pinned) + 1
			pinned = append(pinned, opt)
			continue
		}
		rest = append(rest, opt)
	}
	if len(pinned) == 0 {
		return options
	}
	return append(pinned, rest...)
}

// filterVisible reports whether the filter input belongs on screen: whenever it
// holds text, or whenever the cursor is somewhere the filter actually applies.
// A list with no pinned rows always shows it.
func (m filterMenuModel) filterVisible() bool {
	if strings.TrimSpace(m.input.Value()) != "" {
		return true
	}
	return !m.onPinnedRow()
}

// onPinnedRow reports whether the cursor sits on a pinned row, i.e. outside the
// filterable part of the list.
func (m filterMenuModel) onPinnedRow() bool {
	if len(m.filtered) == 0 {
		return false
	}
	return m.options[m.filtered[m.cursor]].Pinned
}

// pinnedIndices returns the option indices of the pinned rows, in order, so a
// digit can select the Nth one.
func (m filterMenuModel) pinnedIndices() []int {
	var out []int
	for i, opt := range m.options {
		if opt.Pinned {
			out = append(out, i)
		}
	}
	return out
}

// hasFilterableRows reports whether there is a filterable section at all. When
// the pinned rows are the whole list the hint must not offer to browse one.
func (m filterMenuModel) hasFilterableRows() bool {
	for _, opt := range m.options {
		if !opt.Pinned && !opt.IsHeader {
			return true
		}
	}
	return false
}

// digitIndex maps a 1-9 keypress to a zero-based row index.
func digitIndex(msg tea.KeyMsg) (int, bool) {
	s := msg.String()
	if len(s) != 1 || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	return int(s[0] - '1'), true
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
		// While the cursor is up among the pinned rows the filter is not in play:
		// those rows look like a numbered menu, so they answer to digits like one,
		// and letters are ignored rather than quietly narrowing a list the user is
		// not looking at. Erasing stays allowed from anywhere, or a filter typed
		// in the list and then arrowed away from could never be cleared.
		if m.onPinnedRow() {
			if idx, ok := digitIndex(msg); ok {
				if pinned := m.pinnedIndices(); idx < len(pinned) {
					m.selected = pinned[idx]
					m.done = true
					return m, tea.Quit
				}
				return m, nil
			}
			// Space arrives as its own key type in some bubbletea versions, so
			// swallow it explicitly — a stray space starting a filter from up here
			// is the same bug as a letter doing it.
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				return m, nil
			}
		}
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
	if m.filterVisible() {
		fmt.Fprintf(&b, "  %s\n", m.input.View())
		fmt.Fprintf(&b, "  %s\n\n",
			filterMenuHintStyle.Render("type to filter • ↑↓ navigate • enter select • esc back"))
	} else {
		// No filter box while the cursor sits on a pinned row: it could not
		// filter anything the user is looking at, and showing it would
		// misrepresent what typing does.
		hint := "↑↓ navigate • 1-9 jump • enter select • esc back"
		if m.hasFilterableRows() {
			hint = "↑↓ navigate • 1-9 jump • ↓ to browse the list • enter select • esc back"
		}
		fmt.Fprintf(&b, "  %s\n\n", filterMenuHintStyle.Render(hint))
	}

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

	options = frontLoadPinned(options)
	model := filterMenuModel{
		title:    title,
		options:  options,
		render:   render,
		input:    ti,
		filtered: make([]int, 0, len(options)),
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
