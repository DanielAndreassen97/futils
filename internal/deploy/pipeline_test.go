package deploy

import (
	"strings"
	"testing"
)

// GUID-shaped fixtures: the scanner only sees canonical GUIDs, so tests use
// full fake GUIDs and the index keys must match them.
const (
	gWSDev  = "d0d00000-1111-4222-8333-444455556666"
	gWSTst  = "e1e10000-1111-4222-8333-444455556666"
	gLHDev  = "c0de0000-1111-4222-8333-444455556666"
	gLHTst  = "f00d0000-1111-4222-8333-444455556666"
	gUnkwn  = "9999e000-1111-4222-8333-444455556666"
	gShared = "aaaa0000-1111-4222-8333-444455556666" // shared reference workspace: maps to itself
)

func pipelineRebinderGUIDs() *Rebinder {
	base := idx(IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev})
	tgt := idx(IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: gLHTst, WorkspaceID: gWSTst})
	return &Rebinder{
		baseline: base, target: tgt, overrides: map[string]Override{},
		wsMap:           map[string]string{gWSDev: gWSTst},
		wsAmbiguous:     map[string]bool{},
		baselineWSNames: map[string]string{gWSDev: "DW - DEV - Data"},
		targetWSNames:   map[string]string{gWSTst: "DW - TEST - Data"},
	}
}

func TestRebindPipelineWorkspaceGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"WorkspaceID":{"defaultValue":"` + gWSDev + `"}}`)
	out, outcome := rb.RebindPipeline(in)
	if !strings.Contains(string(out), gWSTst) || strings.Contains(string(out), gWSDev) {
		t.Fatalf("workspace GUID not rewritten: %s", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Kind != "Workspace" || outcome.Changes[0].Name != "DW - TEST - Data" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestRebindPipelineItemGUID(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"LakehouseID":"` + gLHDev + `"}`)
	out, outcome := rb.RebindPipeline(in)
	if !strings.Contains(string(out), gLHTst) {
		t.Fatalf("item GUID not rewritten: %s", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Kind != "Lakehouse" || outcome.Changes[0].Name != "LH_Bronze" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestRebindPipelineUnknownGUIDUntouched(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"External":"` + gUnkwn + `"}`)
	out, outcome := rb.RebindPipeline(in)
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("unknown GUID must be untouched: %s %#v", out, outcome.Changes)
	}
}

func TestRebindPartDispatchesPipeline(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"WorkspaceID":"` + gWSDev + `"}`)
	out, _ := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "PL_Copy"}, "pipeline-content.json", in, "")
	if !strings.Contains(string(out), gWSTst) {
		t.Fatalf("RebindPart must dispatch DataPipeline parts: %s", out)
	}
}

func TestRebindPipelineEndpointHost(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	rb.client = &fakeFabric{sqlByLH: map[string][2]string{
		gLHDev: {"aaa111-dev.datawarehouse.fabric.microsoft.com", "ep-dev"},
		gLHTst: {"bbb222-test.datawarehouse.fabric.microsoft.com", "ep-test"},
	}}
	in := []byte(`{"server":"aaa111-dev.datawarehouse.fabric.microsoft.com"}`)
	out, outcome := rb.RebindPipeline(in)
	if !strings.Contains(string(out), "bbb222-test.datawarehouse.fabric.microsoft.com") {
		t.Fatalf("host not rewritten: %s", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Kind != "SQL endpoint" || outcome.Changes[0].Name != "LH_Bronze" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestRebindPipelineUnknownHostUntouched(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	rb.client = &fakeFabric{sqlByLH: map[string][2]string{}}
	in := []byte(`{"server":"someone-elses.datawarehouse.fabric.microsoft.com"}`)
	out, outcome := rb.RebindPipeline(in)
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("unknown host must be untouched: %s %#v", out, outcome.Changes)
	}
}

// TestRebindPipelineHostNotInTargetLeavesUntouchedNoUnresolved proves the
// host loop never reports its own UnresolvedRef, mirroring the GUID branch:
// the leftover scan is the single reporting channel for every unresolved
// reference, host or GUID, so this pass must stay silent even when it
// recognizes the baseline owner but finds no same-named lakehouse in the
// target.
func TestRebindPipelineHostNotInTargetLeavesUntouchedNoUnresolved(t *testing.T) {
	base := idx(IndexedItem{Name: "LH_OnlyDev", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev})
	tgt := idx() // nothing in target — no same-named lakehouse
	rb := &Rebinder{
		baseline: base, target: tgt, overrides: map[string]Override{},
		wsMap: map[string]string{}, wsAmbiguous: map[string]bool{}, baselineWSNames: map[string]string{},
		client: &fakeFabric{sqlByLH: map[string][2]string{gLHDev: {"aaa111-dev.datawarehouse.fabric.microsoft.com", "ep-dev"}}},
	}
	in := []byte(`{"server":"aaa111-dev.datawarehouse.fabric.microsoft.com"}`)
	out, outcome := rb.RebindPipeline(in)
	if string(out) != string(in) {
		t.Fatalf("host with no target match must be left untouched: %s", out)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("RebindPipeline must not report unresolved hosts — the leftover scan owns that: %#v", outcome.Unresolved)
	}
}

func TestSubstitutePartsThreadsTargetWorkspaceToPipeline(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	item := LocalItem{Type: "DataPipeline", DisplayName: "PL_Main", Parts: []Part{
		{Path: "pipeline-content.json", Content: []byte(`{"workspaceId":"` + placeholderGUID + `"}`)},
	}}
	parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(parts["pipeline-content.json"]), gWSTst) {
		t.Fatalf("target workspace not applied through SubstituteParts: %s", parts["pipeline-content.json"])
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("zero GUID must not be reported as unresolved: %#v", outcome.Unresolved)
	}
}

// A bare host belongs to a workspace, so several baseline lakehouses own it.
// It must resolve the same way on every run, whichever order the baseline
// index is walked in: through every owner, rewriting only when the owners that
// exist in the target agree on one endpoint. sharedHostRebinder (semmod_test.go)
// gives LH_Bronze and LH_Gold one DEV host and different TEST hosts.
func TestRebindPipelineSharedHostOnlyOneOwnerInTarget(t *testing.T) {
	in := []byte(`{"server":"` + paramDevHost + `"}`)
	for i := 0; i < 20; i++ {
		rb := sharedHostRebinder(true)
		rb.target = idx(IndexedItem{Name: "LH_Gold", Type: "Lakehouse", GUID: gLHGoldTst, WorkspaceID: gWSTst})
		out, outcome := rb.RebindPipeline(in)
		if string(out) != `{"server":"`+goldTstHost+`"}` {
			t.Fatalf("run %d: host must follow the one owner present in the target: %s", i, out)
		}
		if len(outcome.Changes) != 1 || outcome.Changes[0].Name != "LH_Gold" {
			t.Fatalf("run %d: changes = %#v", i, outcome.Changes)
		}
	}
}

func TestRebindPipelineSharedHostSplitInTargetIsLeftAndWarned(t *testing.T) {
	in := []byte(`{"server":"` + paramDevHost + `"}`)
	for i := 0; i < 20; i++ {
		rb := sharedHostRebinder(true) // LH_Bronze → TEST host, LH_Gold → gold host
		out, outcome := rb.RebindPipeline(in)
		if string(out) != string(in) || len(outcome.Changes) != 0 {
			t.Fatalf("run %d: owners on different target endpoints must leave the host: %s %#v", i, out, outcome.Changes)
		}
		refs := rb.ScanLeftovers("pipeline-content.json", out)
		if len(refs) != 1 || !strings.Contains(refs[0].Hint, "ambiguous") || !strings.Contains(refs[0].Hint, `"LH_Bronze", "LH_Gold"`) {
			t.Fatalf("run %d: want one ambiguous leftover naming both owners: %#v", i, refs)
		}
	}
}

func TestRebindPipelineSharedHostSameTargetRewrites(t *testing.T) {
	rb := sharedHostRebinder(true)
	rb.client.(*fakeFabric).sqlByLH[gLHGoldTst] = [2]string{paramTstHost, "ep-gold-test"}
	out, outcome := rb.RebindPipeline([]byte(`{"server":"` + paramDevHost + `"}`))
	if string(out) != `{"server":"`+paramTstHost+`"}` {
		t.Fatalf("owners agreeing on one target endpoint must rewrite: %s", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Name != "LH_Bronze, LH_Gold" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}
