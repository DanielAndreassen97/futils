package deploy

import (
	"strings"
	"testing"
)

func TestScanLeftoversWorkspaceGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	refs := rb.ScanLeftovers("notebook-content.py", []byte(`path = "abfss://`+gWSDev+`@onelake"`))
	if len(refs) != 1 || refs[0].Reason != ReasonLeftover || refs[0].GUID != gWSDev {
		t.Fatalf("refs = %#v", refs)
	}
	if !strings.Contains(refs[0].Hint, "DW - DEV - Data") || !strings.Contains(refs[0].Hint, gWSTst) {
		t.Errorf("hint must name the workspace and the target value: %q", refs[0].Hint)
	}
}

func TestScanLeftoversItemGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	refs := rb.ScanLeftovers("notebook-content.py", []byte(gLHDev))
	if len(refs) != 1 || !strings.Contains(refs[0].Hint, "LH_Bronze") {
		t.Fatalf("refs = %#v", refs)
	}
}

func TestScanLeftoversIgnoresUnknownAndIdentity(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	// unknown GUID: not baseline's -> silent
	if refs := rb.ScanLeftovers("p", []byte(gUnkwn)); len(refs) != 0 {
		t.Fatalf("unknown GUID must not warn: %#v", refs)
	}
	// shared reference workspace (same GUID both envs): identity -> silent
	rb.wsMap[gShared] = gShared
	rb.baselineWSNames[gShared] = "Shared reference workspace"
	if refs := rb.ScanLeftovers("p", []byte(gShared)); len(refs) != 0 {
		t.Fatalf("identity mapping must not warn: %#v", refs)
	}
}

// TestScanLeftoversHostNoMatchingLakehouseInTarget covers the case pipeline.go
// leaves silent (owner resolves, but no same-named lakehouse exists in the
// target): the leftover scan is the only reporter for this host, so its hint
// must say why it's unresolved, not just name the owner.
func TestScanLeftoversHostNoMatchingLakehouseInTarget(t *testing.T) {
	base := idx(IndexedItem{Name: "LH_OnlyDev", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev})
	tgt := idx() // nothing in target — no same-named lakehouse
	rb := &Rebinder{
		baseline: base, target: tgt, overrides: map[string]Override{},
		wsMap: map[string]string{}, wsAmbiguous: map[string]bool{}, baselineWSNames: map[string]string{},
		client: &fakeFabric{sqlByLH: map[string][2]string{gLHDev: {"aaa111-dev.datawarehouse.fabric.microsoft.com", "ep-dev"}}},
	}
	refs := rb.ScanLeftovers("p", []byte(`{"server":"aaa111-dev.datawarehouse.fabric.microsoft.com"}`))
	if len(refs) != 1 || refs[0].Reason != ReasonLeftover || refs[0].ItemType != "SQL endpoint" {
		t.Fatalf("refs = %#v", refs)
	}
	if !strings.Contains(refs[0].Hint, "LH_OnlyDev") || !strings.Contains(refs[0].Hint, "no same-named lakehouse in the target") {
		t.Errorf("hint must name the owner and explain why it can't resolve: %q", refs[0].Hint)
	}
}

// TestScanLeftoversItemAmbiguousHint covers finding #4: when the name IS
// known in the target but matches items in more than one target workspace,
// the hint must say so — not fall back to the "no same-named item" wording,
// which would blame absence for what's actually an ambiguity problem.
func TestScanLeftoversItemAmbiguousHint(t *testing.T) {
	base := idx(IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev})
	tgt := idx(
		IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: "t1111111-1111-4222-8333-444455556666", WorkspaceID: "ws-1"},
		IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: "t2222222-1111-4222-8333-444455556666", WorkspaceID: "ws-2"},
	)
	rb := &Rebinder{
		baseline: base, target: tgt, overrides: map[string]Override{},
		wsMap: map[string]string{}, wsAmbiguous: map[string]bool{}, baselineWSNames: map[string]string{},
	}
	refs := rb.ScanLeftovers("p", []byte(gLHDev))
	if len(refs) != 1 {
		t.Fatalf("refs = %#v", refs)
	}
	if !strings.Contains(refs[0].Hint, "ambiguous") {
		t.Errorf("ambiguous target name must say so: %q", refs[0].Hint)
	}
	if strings.Contains(refs[0].Hint, "no same-named item in the target") {
		t.Errorf("must not reuse the absent-item wording for an ambiguous name: %q", refs[0].Hint)
	}
}

// TestScanLeftoversStableLocationDedupesAcrossParts covers finding #2: without
// a stable Location, the same broken GUID surfacing in every part of one item
// (a semantic model's 40 table expressions, say) would report as one
// UnresolvedRef PER PART instead of collapsing via AddUnresolved's (GUID,
// ItemType, Location) key. This mirrors exactly how SubstituteParts feeds
// ScanLeftovers' output into outcome.AddUnresolved, part by part.
func TestScanLeftoversStableLocationDedupesAcrossParts(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	var out RebindOutcome
	for _, part := range []string{"part1.json", "part2.json"} {
		for _, u := range rb.ScanLeftovers(part, []byte(gLHDev)) {
			out.AddUnresolved(u)
		}
	}
	if len(out.Unresolved) != 1 {
		t.Fatalf("expected one deduped ref across two parts, got %d: %#v", len(out.Unresolved), out.Unresolved)
	}
	if out.Unresolved[0].Count != 2 {
		t.Errorf("expected Count 2 after two parts, got %d", out.Unresolved[0].Count)
	}
	if !strings.Contains(out.Unresolved[0].Hint, "part1.json") {
		t.Errorf("hint should carry the first occurrence's part path: %q", out.Unresolved[0].Hint)
	}
}

func TestScanLeftoversNeverMutates(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(gWSDev)
	rb.ScanLeftovers("p", in)
	if string(in) != gWSDev {
		t.Fatal("scan must not mutate content")
	}
}
