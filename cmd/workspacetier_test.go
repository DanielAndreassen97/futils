package cmd

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

func tierCaps() []fabric.Capacity {
	return []fabric.Capacity{
		{ID: "f", DisplayName: "Fabric cap", SKU: "F128", Region: "North Europe"},
		{ID: "ppu", DisplayName: "PPU cap", SKU: "PP3", Region: "North Europe"},
		{ID: "prem", DisplayName: "Premium cap", SKU: "P3", Region: "North Europe"},
		{ID: "emb", DisplayName: "Embedded cap", SKU: "EM2", Region: "North Europe"},
		{ID: "az", DisplayName: "Azure cap", SKU: "A4", Region: "North Europe"},
		{ID: "odd", DisplayName: "Future cap", SKU: "ZZ9", Region: "North Europe"},
	}
}

func TestClassifyWorkspaceByCapacitySKU(t *testing.T) {
	cases := []struct {
		name     string
		ws       fabric.Workspace
		wantTier string
		wantSKU  string
	}{
		{"fabric", fabric.Workspace{Type: "Workspace", CapacityID: "f"}, "Fabric", "F128"},
		// PP must be tested before P, or a Premium Per User capacity reads as
		// plain Premium — both SKUs start with the same letter.
		{"premium per user", fabric.Workspace{Type: "Workspace", CapacityID: "ppu"}, "PPU", "PP3"},
		{"premium", fabric.Workspace{Type: "Workspace", CapacityID: "prem"}, "Premium", "P3"},
		{"embedded EM", fabric.Workspace{Type: "Workspace", CapacityID: "emb"}, "Embedded", "EM2"},
		{"embedded A", fabric.Workspace{Type: "Workspace", CapacityID: "az"}, "Embedded", "A4"},
		// An unrecognised SKU family must degrade to "there is a capacity",
		// never to a wrong tier name.
		{"unknown family", fabric.Workspace{Type: "Workspace", CapacityID: "odd"}, "Capacity", "ZZ9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyWorkspace(c.ws, tierCaps())
			if got.Name != c.wantTier || got.SKU != c.wantSKU {
				t.Errorf("classifyWorkspace = %q/%q, want %q/%q", got.Name, got.SKU, c.wantTier, c.wantSKU)
			}
		})
	}
}

func TestClassifyWorkspaceWithoutCapacityIsPro(t *testing.T) {
	// A workspace on shared capacity is what everyone calls a Pro workspace.
	// The old UI said "no capacity", which described futils' knowledge rather
	// than the workspace.
	got := classifyWorkspace(fabric.Workspace{Type: "Workspace"}, tierCaps())
	if got.Name != "Pro" {
		t.Errorf("tier = %q, want Pro", got.Name)
	}
	if got.SKU != "" {
		t.Errorf("SKU = %q, want empty — a Pro workspace has no capacity SKU", got.SKU)
	}
}

func TestClassifyWorkspacePersonalBeatsCapacity(t *testing.T) {
	// A capacity admin's My workspace gets assigned to a capacity, but it is
	// still a personal workspace and that is the more useful label.
	got := classifyWorkspace(fabric.Workspace{Type: "Personal", CapacityID: "f"}, tierCaps())
	if got.Name != "Personal" {
		t.Errorf("tier = %q, want Personal", got.Name)
	}
}

func TestClassifyWorkspaceUnresolvableCapacity(t *testing.T) {
	// The workspace has a capacity we cannot name — that is not the same as
	// having none, and it must never render as Pro.
	got := classifyWorkspace(fabric.Workspace{Type: "Workspace", CapacityID: "not-in-list"}, tierCaps())
	if got.Name != "Capacity" {
		t.Errorf("tier = %q, want Capacity", got.Name)
	}
	if got.SKU != "" {
		t.Errorf("SKU = %q, want empty when the capacity is unresolvable", got.SKU)
	}
}

func TestClassifyWorkspaceSKUMatchingIsCaseInsensitive(t *testing.T) {
	caps := []fabric.Capacity{{ID: "c", SKU: "f64"}}
	if got := classifyWorkspace(fabric.Workspace{Type: "Workspace", CapacityID: "c"}, caps); got.Name != "Fabric" {
		t.Errorf("tier = %q, want Fabric for a lower-case SKU", got.Name)
	}
}

func TestTierLabelPadsToTheWidestName(t *testing.T) {
	// The tier column has to align down the list, and every tier name happens
	// to fit the same width — a regression here would ragged the whole column.
	for _, name := range []string{"Fabric", "Pro", "PPU", "Premium", "Embedded", "Personal", "Capacity"} {
		if len(name) > tierColW {
			t.Errorf("tier name %q is wider than the column (%d)", name, tierColW)
		}
	}
}

func TestRenderWorkspaceRowAlignsNonASCIINames(t *testing.T) {
	// "DW - Ærlig & Øst" is a real workspace name. FitWidth counts runes,
	// so æ costs one column like any other letter — a byte-based pad would
	// short the row and ragged the whole tier column.
	caps := []fabric.Capacity{{ID: "c", SKU: "F128"}}
	rows := []string{"DW - Core", "DW - Ærlig & Øst", "DW - Reports - Contoso - TEST"}

	var starts []int
	for _, name := range rows {
		opt := ui.FilterOption{
			Label: name,
			Value: "id",
			Meta:  classifyWorkspace(fabric.Workspace{Type: "Workspace", CapacityID: "c"}, caps),
		}
		rendered := []rune(renderWorkspaceRow(opt, false))
		starts = append(starts, indexOfRunes(rendered, []rune("Fabric")))
	}
	for i, got := range starts {
		if got < 0 {
			t.Fatalf("row %q did not render its tier", rows[i])
		}
		if got != starts[0] {
			t.Errorf("tier column for %q starts at %d, but %q starts at %d",
				rows[i], got, rows[0], starts[0])
		}
	}
}

// indexOfRunes is strings.Index in rune space — a byte index would defeat the
// point of the test it serves.
func indexOfRunes(haystack, needle []rune) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestRenderRoleBarUsesADistinctShadePerRole(t *testing.T) {
	// The whole point of the ladder is that Admin and Viewer do not look alike.
	seen := map[lipgloss.Color]string{}
	for _, role := range wsRoles {
		c, ok := wsRoleBarPalette[role]
		if !ok {
			t.Fatalf("role %q has no bar colour", role)
		}
		if prev, dup := seen[c.bg]; dup {
			t.Errorf("roles %q and %q share the background %q", prev, role, c.bg)
		}
		seen[c.bg] = role
	}
}

func TestRenderRoleBarFallsBackForAnUnknownRole(t *testing.T) {
	// wsRoleNone is futils' own bucket and deliberately has no ladder entry;
	// rendering it must not panic or produce an unstyled row.
	if got := renderRoleBar(wsRoleNone, "NO WORKSPACE ROLE · 1"); !strings.Contains(got, "NO WORKSPACE ROLE") {
		t.Errorf("fallback bar = %q", got)
	}
}

func TestRenderWorkspaceRowSelectedSpansTheFullWidth(t *testing.T) {
	// The highlight is a background, so it only reads as a bar if the row is
	// padded out. A row that stops at its content leaves a ragged block.
	caps := []fabric.Capacity{{ID: "c", SKU: "F128"}}
	opt := ui.FilterOption{
		Label: "DW - Core",
		Value: "id",
		Meta:  classifyWorkspace(fabric.Workspace{Type: "Workspace", CapacityID: "c"}, caps),
	}

	if got, want := lipgloss.Width(renderWorkspaceRow(opt, true)), wsBarWidth(); got != want {
		t.Errorf("selected row is %d columns wide, want %d", got, want)
	}
	// An unselected row must NOT be padded out — a full-width unselected row
	// would paint the terminal background over anything to its right.
	if got := lipgloss.Width(renderWorkspaceRow(opt, false)); got >= wsBarWidth() {
		t.Errorf("unselected row is %d columns wide, want less than %d", got, wsBarWidth())
	}
}

func TestRenderWorkspaceRowSelectedMatchesTheHeadingWidth(t *testing.T) {
	// Cursor bar and role bar are the same visual device; different widths
	// would make the list look misaligned as the cursor moves past a heading.
	opt := ui.FilterOption{Label: "DW - Core", Value: "id", Meta: workspaceTier{Name: "Pro"}}
	row := lipgloss.Width(renderWorkspaceRow(opt, true))
	bar := lipgloss.Width(renderRoleBar(wsRoleAdmin, "ADMIN · 34"))
	if row != bar {
		t.Errorf("cursor bar is %d wide but the role bar is %d", row, bar)
	}
}
