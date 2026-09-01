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
	out, outcome := rb.RebindPipeline(in, gWSTst)
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
	out, outcome := rb.RebindPipeline(in, gWSTst)
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
	out, outcome := rb.RebindPipeline(in, gWSTst)
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("unknown GUID must be untouched: %s %#v", out, outcome.Changes)
	}
}

func TestRebindPartDispatchesPipeline(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"WorkspaceID":"` + gWSDev + `"}`)
	out, _ := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "PL_Copy"}, "pipeline-content.json", in, gWSTst)
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
	out, outcome := rb.RebindPipeline(in, gWSTst)
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
	out, outcome := rb.RebindPipeline(in, gWSTst)
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
	out, outcome := rb.RebindPipeline(in, gWSTst)
	if string(out) != string(in) {
		t.Fatalf("host with no target match must be left untouched: %s", out)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("RebindPipeline must not report unresolved hosts — the leftover scan owns that: %#v", outcome.Unresolved)
	}
}

// A same-workspace reference is serialized by Fabric git as an all-zeros
// workspaceId; the items REST API stores it verbatim, so the deploy has to
// substitute the target workspace the way git-sync would.
func TestRebindPipelineSameWorkspacePlaceholder(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"typeProperties":{"workspaceId":"` + placeholderGUID + `","pipelineId":"` + gLHDev + `"}}`)
	out, outcome := rb.RebindPipeline(in, gWSTst)
	if strings.Contains(string(out), placeholderGUID) {
		t.Fatalf("same-workspace placeholder not resolved: %s", out)
	}
	if !strings.Contains(string(out), `"workspaceId":"`+gWSTst+`"`) {
		t.Fatalf("workspaceId not set to the target workspace: %s", out)
	}
	var found bool
	for _, c := range outcome.Changes {
		if c.Old == placeholderGUID {
			found = true
			if c.Kind != "Workspace" || c.New != gWSTst || c.Name != "DW - TEST - Data" {
				t.Fatalf("placeholder change = %#v", c)
			}
		}
	}
	if !found {
		t.Fatalf("placeholder rewrite not recorded: %#v", outcome.Changes)
	}
}

// The rewrite is anchored on the workspaceId field: any OTHER all-zeros value
// in the payload (a self-referencing itemId, an unset connection id) means
// something different and must survive untouched.
func TestRebindPipelineSameWorkspacePlaceholderScopedToField(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"itemId":"` + placeholderGUID + `","connectionId":"` + placeholderGUID + `"}`)
	out, outcome := rb.RebindPipeline(in, gWSTst)
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("non-workspaceId zero GUIDs must be untouched: %s %#v", out, outcome.Changes)
	}
}

// With no target workspace in scope (a rebind preview) the placeholder is left
// alone rather than resolved to something arbitrary.
func TestRebindPipelineSameWorkspaceNoTargetLeavesPlaceholder(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"workspaceId":"` + placeholderGUID + `"}`)
	out, outcome := rb.RebindPipeline(in, "")
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("placeholder must be left alone without a target workspace: %s %#v", out, outcome.Changes)
	}
}

// Only the pipeline pass resolves the placeholder — a notebook or semantic
// model carrying an all-zeros GUID is not a same-workspace reference.
func TestRebindPartResolvesPlaceholderOnlyForPipelines(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"metadata":{"dependencies":{"workspaceId":"` + placeholderGUID + `"}}}`)
	out, _ := rb.RebindPart(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", in, gWSTst)
	if string(out) != string(in) {
		t.Fatalf("notebook part must be untouched: %s", out)
	}
}
