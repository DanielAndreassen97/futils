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

func TestScanLeftoversNeverMutates(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(gWSDev)
	rb.ScanLeftovers("p", in)
	if string(in) != gWSDev {
		t.Fatal("scan must not mutate content")
	}
}
