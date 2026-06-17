package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
)

// dirPickerModel wraps bubbles' filepicker for directory-only selection,
// adding futils' esc=back / ctrl+c=quit conventions. Enter selects the
// highlighted directory; l/→ descends; h/←/backspace goes up.
type dirPickerModel struct {
	title    string
	fp       filepicker.Model
	selected string
	goBack   bool
	quit     bool
}

func (m dirPickerModel) Init() tea.Cmd { return m.fp.Init() }

func (m dirPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			m.quit = true
			return m, tea.Quit
		case "esc":
			m.goBack = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.fp, cmd = m.fp.Update(msg)
	if didSelect, path := m.fp.DidSelectFile(msg); didSelect {
		m.selected = path
		return m, tea.Quit
	}
	return m, cmd
}

func (m dirPickerModel) View() string {
	hint := "↑↓/jk move • →/l open • ←/h up • enter select • esc back"
	return fmt.Sprintf("  %s\n  %s\n\n%s", m.title, checkboxHintStyle.Render(hint), m.fp.View())
}

// PickDirectory shows an in-terminal directory browser rooted at startDir and
// returns the absolute path the user selects. Returns ErrGoBack on esc and
// ErrQuit on ctrl+c.
func PickDirectory(title, startDir string) (string, error) {
	fp := filepicker.New()
	fp.CurrentDirectory = startDir
	fp.DirAllowed = true
	fp.FileAllowed = false
	fp.AutoHeight = false
	fp.SetHeight(14)

	model := dirPickerModel{title: title, fp: fp}
	final, err := tea.NewProgram(model).Run()
	if err != nil {
		return "", err
	}
	result := final.(dirPickerModel)
	if result.quit {
		return "", ErrQuit
	}
	if result.goBack {
		return "", ErrGoBack
	}
	return result.selected, nil
}
