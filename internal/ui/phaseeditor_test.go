// Tests for the phase editor: the list where every post-deploy run shows its
// phase and the user changes it in place.
//
// Why: the phases decide which notebooks run together in one Spark session and
// which wait for others. A key that moves the wrong row, lets a phase drop
// below 1, or loses edits on enter would silently run a config notebook in
// parallel with the scripts that depend on it.
//
// Example: rows [NB_Config 1, NB_A 1]; down, right → [NB_Config 1, NB_A 2].
package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func phaseKey(m phaseEditorModel, k tea.KeyMsg) phaseEditorModel {
	next, _ := m.Update(k)
	return next.(phaseEditorModel)
}

func TestPhaseEditorArrowsAndDigitsChangeCursorRow(t *testing.T) {
	m := newPhaseEditorModel("t", []string{"NB_Config", "NB_A", "NB_B"}, []int{1, 1, 1})
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyDown})
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyRight})
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyDown})
	m = phaseKey(m, keyMsg("3"))
	if want := []int{1, 2, 3}; !reflect.DeepEqual(m.phases, want) {
		t.Fatalf("phases = %v, want %v", m.phases, want)
	}
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.phases[2] != 2 {
		t.Fatalf("left must lower the phase, got %d", m.phases[2])
	}
}

func TestPhaseEditorPhaseStaysWithinOneToNine(t *testing.T) {
	m := newPhaseEditorModel("t", []string{"A"}, []int{1})
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.phases[0] != 1 {
		t.Fatalf("phase went below 1: %d", m.phases[0])
	}
	m = phaseKey(m, keyMsg("9"))
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.phases[0] != 9 {
		t.Fatalf("phase went above 9: %d", m.phases[0])
	}
	m = phaseKey(m, keyMsg("0"))
	if m.phases[0] != 9 {
		t.Fatalf("0 must be ignored, got %d", m.phases[0])
	}
}

func TestPhaseEditorEnterKeepsEditsEscDiscards(t *testing.T) {
	m := newPhaseEditorModel("t", []string{"A", "B"}, []int{1, 1})
	m = phaseKey(m, keyMsg("2"))
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done || m.goBack || m.phases[0] != 2 {
		t.Fatalf("enter must finish with edits kept: done=%v back=%v phases=%v", m.done, m.goBack, m.phases)
	}

	m = newPhaseEditorModel("t", []string{"A"}, []int{1})
	m = phaseKey(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.goBack {
		t.Fatal("esc must go back")
	}
}

func TestPhaseEditorViewListsEveryRowWithItsPhase(t *testing.T) {
	m := newPhaseEditorModel("Phases", []string{"NB_Config", "NB_Long_Name"}, []int{1, 2})
	v := m.View()
	for _, want := range []string{"NB_Config", "NB_Long_Name", "Run order 1: 1", "Run order 2: 1"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

// A run order above the editor's 1-9 range (hand-edited config) stays last
// instead of being reset to 1, which would run it first.
func TestPhaseEditorCapsHighPhase(t *testing.T) {
	m := newPhaseEditorModel("t", []string{"NB_Config", "NB_Smoke"}, []int{1, 12})
	if m.phases[1] != maxPhase {
		t.Fatalf("phase 12 = %d in the editor, want %d", m.phases[1], maxPhase)
	}
}
