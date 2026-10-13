package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// maxPhase caps phases at one digit, so a digit key always sets a phase
// directly and the column never needs more than one character.
const maxPhase = 9

var phaseValueStyle = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)

type phaseEditorModel struct {
	title      string
	names      []string
	phases     []int
	cursor     int
	termHeight int
	done       bool
	goBack     bool
	quit       bool
	goHome     bool
}

func newPhaseEditorModel(title string, names []string, phases []int) phaseEditorModel {
	p := make([]int, len(names))
	for i := range p {
		p[i] = 1
		if i < len(phases) && phases[i] >= 1 && phases[i] <= maxPhase {
			p[i] = phases[i]
		}
	}
	return phaseEditorModel{title: title, names: names, phases: p}
}

func (m phaseEditorModel) Init() tea.Cmd { return nil }

func (m phaseEditorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
	case tea.KeyMsg:
		if len(m.names) == 0 {
			m.done = true
			return m, tea.Quit
		}
		switch k := msg.String(); k {
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.names)) % len(m.names)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(m.names)
		case "left", "h", "-":
			if m.phases[m.cursor] > 1 {
				m.phases[m.cursor]--
			}
		case "right", "l", "+":
			if m.phases[m.cursor] < maxPhase {
				m.phases[m.cursor]++
			}
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.phases[m.cursor] = int(k[0] - '0')
		case "enter":
			m.done = true
			return m, tea.Quit
		case "esc", "b":
			m.goBack, m.done = true, true
			return m, tea.Quit
		case "ctrl+c", "q":
			m.quit, m.done = true, true
			return m, tea.Quit
		case "m":
			m.goHome, m.done = true, true
			return m, tea.Quit
		}
	}
	return m, nil
}

// summary counts runs per run order, lowest first: "Run order 1: 2 · Run order 2: 9".
func (m phaseEditorModel) summary() string {
	counts := map[int]int{}
	for _, p := range m.phases {
		counts[p]++
	}
	var parts []string
	for p := 1; p <= maxPhase; p++ {
		if counts[p] > 0 {
			parts = append(parts, fmt.Sprintf("Run order %d: %d", p, counts[p]))
		}
	}
	return strings.Join(parts, " · ")
}

func (m phaseEditorModel) View() string {
	if m.done {
		if m.goBack || m.quit || m.goHome {
			return ""
		}
		return checkboxCheckedBoxStyle.Render(fmt.Sprintf("  %s: %s", m.title, m.summary())) + "\n"
	}
	nameW := 0
	for _, n := range m.names {
		nameW = max(nameW, utf8.RuneCountInString(n))
	}
	var b strings.Builder
	hint := "←/→ change run order • 1-9 set run order • ↑↓ move • enter save • esc back • q quit"
	fmt.Fprintf(&b, "  %s\n", m.title)
	fmt.Fprintf(&b, "  %s\n", checkboxHintStyle.Render(hint))
	fmt.Fprintf(&b, "  %s\n\n", checkboxHintStyle.Render(m.summary()))
	// Header rows above: 4 lines (title, hint, summary, blank).
	windowedList(&b, len(m.names), m.cursor, m.termHeight, 4, checkboxHintStyle, func(i int) string {
		sel := i == m.cursor
		name := m.names[i] + strings.Repeat(" ", nameW-utf8.RuneCountInString(m.names[i]))
		phase := fmt.Sprintf("  %d  ", m.phases[i])
		if sel {
			phase = phaseValueStyle.Render(fmt.Sprintf("‹ %d ›", m.phases[i]))
		}
		return fmt.Sprintf("%s%s   %s", CursorPointer(sel), CursorLabel(name, sel), phase)
	})
	return b.String()
}

// PhaseEditor lists names with their phase and lets the user change each
// phase in place (←/→, or a digit 1-9). phases[i] is names[i]'s starting
// phase; values outside 1-9 start at 1. Returns the edited phases in names
// order, or ErrGoBack / ErrQuit / ErrGoHome when the user leaves without
// saving.
func PhaseEditor(title string, names []string, phases []int) ([]int, error) {
	final, err := tea.NewProgram(newPhaseEditorModel(title, names, phases)).Run()
	if err != nil {
		return nil, err
	}
	m := final.(phaseEditorModel)
	switch {
	case m.quit:
		return nil, ErrQuit
	case m.goHome:
		return nil, ErrGoHome
	case m.goBack:
		return nil, ErrGoBack
	}
	return m.phases, nil
}
