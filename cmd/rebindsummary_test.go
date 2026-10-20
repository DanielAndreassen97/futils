package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
)

func TestCompactValueShortensGUIDsOnly(t *testing.T) {
	if got := compactValue("a1b2c3d4-1111-4222-8333-444455556666"); got != "a1b2c3d4…6666" {
		t.Errorf("GUID: got %q", got)
	}
	if got := compactValue("dev-lh"); got != "dev-lh" {
		t.Errorf("short non-GUID must be untouched: got %q", got)
	}
	if got := compactValue("ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMNOPQRS"); got != "ABCDEFGH…PQRS" {
		t.Errorf("long opaque value: got %q", got)
	}
}

func TestSharedHostSuffixOnlyWhenEveryChangeAgrees(t *testing.T) {
	dom := ".datawarehouse.fabric.microsoft.com"
	same := []deploy.RebindChange{
		{Old: "AAAAAAAAAAAAAAAAAAAA" + dom, New: "bbbbbbbbbbbbbbbbbbbb" + dom},
		{Old: "5e1d0000-1111-4000-8000-0000000000e1", New: "5e1d0000-2222-4000-8000-0000000000e2"}, // GUID rows don't veto
	}
	if got := sharedHostSuffix(same); got != dom {
		t.Errorf("expected shared suffix %q, got %q", dom, got)
	}
	mixed := []deploy.RebindChange{
		{Old: "AAAA" + dom, New: "bbbb.other.example.com"},
	}
	if got := sharedHostSuffix(mixed); got != "" {
		t.Errorf("differing suffixes must not be stripped, got %q", got)
	}
	if got := sharedHostSuffix([]deploy.RebindChange{{Old: "x", New: "y"}}); got != "" {
		t.Errorf("no hosts → no suffix, got %q", got)
	}
}

// One line per reference: name, compact old → compact new. The type header
// carries the count and, for SQL endpoints, the shared domain — so the host
// rows show only the part that actually differs.
func TestPrintRebindSummaryCompactLayout(t *testing.T) {
	dom := ".datawarehouse.fabric.microsoft.com"
	groups := []deployGroup{{Changes: []deploy.RebindChange{
		{Kind: "Lakehouse", Name: "LH_Bronze", Old: "a1b2c3d4-1111-4222-8333-444455556666", New: "f0e1d2c3-1111-4222-8333-444455556666"},
		{Kind: "Lakehouse", Name: "LH_ConfigLog", Old: "b2c3d4e5-1111-4222-8333-444455556666", New: "e1f2a3b4-1111-4222-8333-444455556666"},
		{Kind: "SQL endpoint", Name: "LH_ConfigLog", Old: "ABCDEFGHIJKLMNOPQRSTUVWXYZ" + dom, New: "zyxwvutsrqponmlkjihgfedcba" + dom},
		{Kind: "SQL endpoint", Name: "LH_ConfigLog", Old: "5e1d0000-1111-4000-8000-0000000000e1", New: "5e1d0000-2222-4000-8000-0000000000e2"},
		{Kind: "Workspace", Name: "DW - TEST - Data", Old: "534b9b92-abdc-4fa2-94cc-0b7d50e8d60d", New: "7c3c0fc8-6943-422c-9a0d-79e9a0d28b57"},
	}}}
	out := captureStdout(t, func() { printRebindSummary(groups) })

	if !strings.Contains(out, "5 references will be rebound") {
		t.Errorf("missing plural headline:\n%s", out)
	}
	if strings.Contains(out, "reference(s)") || strings.Contains(out, "Recognized Fabric references") {
		t.Errorf("old wording must be gone:\n%s", out)
	}
	for _, want := range []string{"Lakehouse (2)", "SQL endpoint (2) · *" + dom, "Workspace (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing type header %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "LH_Bronze") || !strings.Contains(out, "a1b2c3d4…6666  →  f0e1d2c3…6666") {
		t.Errorf("expected compact one-line GUID rebind:\n%s", out)
	}
	if strings.Contains(out, "a1b2c3d4-1111") {
		t.Errorf("full GUIDs must not be printed:\n%s", out)
	}
	if !strings.Contains(out, "ABCDEFGH…WXYZ  →  zyxwvuts…dcba") {
		t.Errorf("host rows must drop the shared domain:\n%s", out)
	}
	if strings.Count(out, "LH_ConfigLog") != 2 {
		t.Errorf("a name repeated within one type group prints once (Lakehouse + SQL endpoint = 2):\n%s", out)
	}
	// Arrows within a group align: every old value is padded to the group's widest.
	lines := strings.Split(out, "\n")
	var arrowCols []int
	for _, ln := range lines {
		if strings.Contains(ln, "LH_") && strings.Contains(ln, "→") && !strings.Contains(ln, "…WXYZ") {
			arrowCols = append(arrowCols, strings.Index(ln, "→"))
		}
	}
	if len(arrowCols) < 2 || arrowCols[0] != arrowCols[1] {
		t.Errorf("arrows in the Lakehouse group must align, got columns %v:\n%s", arrowCols, out)
	}
}

func TestPrintRebindSummarySingularHeadline(t *testing.T) {
	groups := []deployGroup{{Changes: []deploy.RebindChange{
		{Kind: "Lakehouse", Name: "LH", Old: "dev-lh", New: "test-lh"},
	}}}
	out := captureStdout(t, func() { printRebindSummary(groups) })
	if !strings.Contains(out, "1 reference will be rebound") {
		t.Errorf("singular headline:\n%s", out)
	}
}

// Each rebind row says which item(s) carry the reference — the summary groups
// by WHAT a value is, so without this a user can't tell where an SQL endpoint
// rewrite lands. Duplicates across items merge into one row naming them all.
func TestPrintRebindSummaryShowsWhereUsed(t *testing.T) {
	groups := []deployGroup{
		{Changes: []deploy.RebindChange{
			{Kind: "Lakehouse", Name: "LH_Gold", Old: "dev-lh", New: "test-lh", Items: []deploy.ItemRef{{Name: "NB_A", Type: "Notebook"}}},
		}},
		{Changes: []deploy.RebindChange{
			{Kind: "Lakehouse", Name: "LH_Gold", Old: "dev-lh", New: "test-lh", Items: []deploy.ItemRef{{Name: "NB_B", Type: "Notebook"}}},
			{Kind: "Workspace", Name: "WS", Old: "dev-ws", New: "test-ws", Items: []deploy.ItemRef{
				{Name: "PL_1", Type: "DataPipeline"}, {Name: "PL_2", Type: "DataPipeline"}, {Name: "Permissions", Type: "SemanticModel"},
			}},
		}},
	}
	out := captureStdout(t, func() { printRebindSummary(groups) })
	if !strings.Contains(out, "in NB_A, NB_B") {
		t.Errorf("merged duplicate must name both items:\n%s", out)
	}
	if !strings.Contains(out, "in 3 items") {
		t.Errorf("more than two items lead with a count:\n%s", out)
	}
	if !strings.Contains(out, "PL_1, PL_2, Permissions (SemanticModel)") {
		t.Errorf("the items behind a count must still be listed, on their own line:\n%s", out)
	}
}

// A workspace reference can sit in every notebook of a mapping. Fifty names
// under one row would bury the summary, so the list stops after a few names
// and points at the diff report, which lists them all.
func TestWhereUsedCapsLongLists(t *testing.T) {
	var items []deploy.ItemRef
	for i := range 52 {
		items = append(items, deploy.ItemRef{Name: fmt.Sprintf("NB_%02d", i), Type: "Notebook"})
	}
	inline, list := whereUsed(items)
	if inline != "in 52 items" {
		t.Errorf("inline = %q", inline)
	}
	want := "NB_00, NB_01, NB_02, NB_03, NB_04, NB_05 + 46 more (all in the diff report)"
	if list != want {
		t.Errorf("list = %q, want %q", list, want)
	}
}

// Items named with a type prefix (NB_, PL_, LH_ …) read on their own; anything
// else gets its type, so a semantic model called "Permissions" isn't mistaken
// for a notebook next to NB_ rows.
func TestItemLabelAddsTypeOnlyWithoutPrefix(t *testing.T) {
	cases := map[deploy.ItemRef]string{
		{Name: "NB_LoadOrders", Type: "Notebook"}:        "NB_LoadOrders",
		{Name: "PL_NotifyTeams", Type: "DataPipeline"}:   "PL_NotifyTeams",
		{Name: "Permissions", Type: "SemanticModel"}:     "Permissions (SemanticModel)",
		{Name: "Sales-FullModel", Type: "SemanticModel"}: "Sales-FullModel (SemanticModel)",
		{Name: "Report for X", Type: "SemanticModel"}:    "Report for X (SemanticModel)",
	}
	for in, want := range cases {
		if got := itemLabel(in); got != want {
			t.Errorf("itemLabel(%+v) = %q, want %q", in, got, want)
		}
	}
}

// An SQL endpoint is rewritten as two values (server host + database id);
// each row says which one it is, instead of two anonymous rows.
func TestPrintRebindSummaryLabelsSQLEndpointParts(t *testing.T) {
	dom := ".datawarehouse.fabric.microsoft.com"
	groups := []deployGroup{{Changes: []deploy.RebindChange{
		{Kind: "SQL endpoint", Name: "LH_ConfigLog", Old: "ABCDEFGHIJKLMNOPQRSTUVWXYZ" + dom, New: "zyxwvutsrqponmlkjihgfedcba" + dom},
		{Kind: "SQL endpoint", Name: "LH_ConfigLog", Old: "5e1d0000-1111-4000-8000-0000000000e1", New: "5e1d0000-2222-4000-8000-0000000000e2"},
	}}}
	out := captureStdout(t, func() { printRebindSummary(groups) })
	if !strings.Contains(out, "host") || !strings.Contains(out, "endpoint id") {
		t.Errorf("SQL endpoint rows must be labelled host / endpoint id:\n%s", out)
	}
}

// The all-zeros GUID is Fabric's "this workspace" placeholder, not a real
// workspace — the row must say so rather than print 00000000…0000.
func TestPrintRebindSummaryNamesZeroWorkspacePlaceholder(t *testing.T) {
	groups := []deployGroup{{Changes: []deploy.RebindChange{
		{Kind: "Workspace", Name: "DW - TEST - Data", Old: "00000000-0000-0000-0000-000000000000", New: "7e577e57-0000-4000-8000-0000000da7a1"},
	}}}
	out := captureStdout(t, func() { printRebindSummary(groups) })
	if strings.Contains(out, "00000000…0000") || !strings.Contains(out, "(this workspace)") {
		t.Errorf("zero GUID must render as the placeholder it is:\n%s", out)
	}
}
