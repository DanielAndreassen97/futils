package cmd

import (
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
		{Old: "ad46c76a-9c2b-4985-bc3c-c8869c466f06", New: "a33b563a-07f6-48e7-b7cd-7e146dbb9566"}, // GUID rows don't veto
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
		{Kind: "SQL endpoint", Name: "LH_ConfigLog", Old: "ad46c76a-9c2b-4985-bc3c-c8869c466f06", New: "a33b563a-07f6-48e7-b7cd-7e146dbb9566"},
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
