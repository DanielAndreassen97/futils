package deploy

import (
	"strings"
	"testing"
)

// GUID-shaped fixtures: the scanner only sees canonical GUIDs, so tests use
// full fake GUIDs and the index keys must match them.
const (
	gWSDev = "534b0000-1111-4222-8333-444455556666"
	gWSTst = "7c3c0000-1111-4222-8333-444455556666"
	gLHDev = "c0de0000-1111-4222-8333-444455556666"
	gLHTst = "f00d0000-1111-4222-8333-444455556666"
	gUnkwn = "9999e000-1111-4222-8333-444455556666"
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
	out, _ := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "PL_Copy"}, "pipeline-content.json", in)
	if !strings.Contains(string(out), gWSTst) {
		t.Fatalf("RebindPart must dispatch DataPipeline parts: %s", out)
	}
}
