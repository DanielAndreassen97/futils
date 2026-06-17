package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDirPickerEscGoesBack(t *testing.T) {
	m := dirPickerModel{}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(dirPickerModel)
	if !got.goBack || got.quit {
		t.Errorf("esc should set goBack: %+v", got)
	}
}

func TestDirPickerCtrlCQuits(t *testing.T) {
	m := dirPickerModel{}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := updated.(dirPickerModel)
	if !got.quit || got.goBack {
		t.Errorf("ctrl+c should set quit: %+v", got)
	}
}
