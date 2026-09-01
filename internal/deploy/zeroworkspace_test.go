package deploy

import (
	"strings"
	"testing"
)

// Fabric's git export writes the all-zeros GUID for "the workspace this item
// lives in". The items API stores it verbatim, so deploy must resolve it to the
// item's target workspace — but only behind a workspace key (fabric-cicd's
// WORKSPACE_ID_REFERENCE_REGEX): the same value elsewhere means something else.
func TestZeroWorkspaceRewritesOnlyWorkspaceKeys(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{
	  "activities": [{"typeProperties": {"workspaceId": "` + placeholderGUID + `", "notebookId": "` + gLHDev + `"}}],
	  "parameters": {"EmptyGuid": {"defaultValue": "` + placeholderGUID + `", "type": "string"}},
	  "other": "` + placeholderGUID + `"
	}`)
	out, outcome := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "PL"}, "pipeline-content.json", in, gWSTst)
	s := string(out)
	if !strings.Contains(s, `"workspaceId": "`+gWSTst+`"`) {
		t.Fatalf("workspaceId zero GUID not rewritten to target workspace:\n%s", s)
	}
	if strings.Count(s, placeholderGUID) != 2 {
		t.Fatalf("zero GUID outside a workspace key (parameter default, other) must survive, got:\n%s", s)
	}
	var ws int
	for _, c := range outcome.Changes {
		if c.Kind == "Workspace" && c.Old == placeholderGUID && c.New == gWSTst && c.Name == "DW - TEST - Data" {
			ws++
		}
	}
	if ws != 1 {
		t.Fatalf("expected exactly one Workspace change for the zero GUID, changes = %#v", outcome.Changes)
	}
}

func TestZeroWorkspaceNoTargetIsNoop(t *testing.T) {
	rb := pipelineRebinderGUIDs()
	in := []byte(`{"workspaceId": "` + placeholderGUID + `"}`)
	out, outcome := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "PL"}, "pipeline-content.json", in, "")
	if string(out) != string(in) || len(outcome.Changes) != 0 {
		t.Fatalf("zero GUID must be untouched without a target workspace: %s %#v", out, outcome.Changes)
	}
}

// A notebook's default_lakehouse_workspace_id can be the zero GUID too. It is
// rewritten in the META block only — a zero GUID in a code cell is code.
func TestZeroWorkspaceNotebookMetaOnlyNotCode(t *testing.T) {
	rb := newRebindFixture(t, nil)
	nb := string(rebindNotebook(devConfigLH, "00000000-0000-0000-0000-000000000000", "LH_ConfigLog", devSilverLH)) +
		"\n# CELL ********************\n\nEMPTY = \"00000000-0000-0000-0000-000000000000\"\n"
	out, outcome := rb.RebindPart(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte(nb), "test-config")
	s := string(out)
	if !strings.Contains(s, `"default_lakehouse_workspace_id": "test-config"`) {
		t.Errorf("META workspace id not rebound to the target workspace:\n%s", s)
	}
	if !strings.Contains(s, `EMPTY = "00000000-0000-0000-0000-000000000000"`) {
		t.Errorf("zero GUID in a code cell must survive:\n%s", s)
	}
	for _, c := range outcome.Changes {
		if c.Old == "00000000-0000-0000-0000-000000000000" && c.Kind != "Workspace" {
			t.Errorf("unexpected non-workspace change on the zero GUID: %#v", c)
		}
	}
}

// shortcuts.metadata.json is excluded: RebindShortcuts already writes the
// resolved item's workspace per real target, and a self-reference (zero
// itemId + zero workspaceId) is what Fabric expects — see that pass.
func TestZeroWorkspaceSkipsShortcuts(t *testing.T) {
	rb := newRebindFixture(t, nil)
	zero := "00000000-0000-0000-0000-000000000000"
	shortcuts := []byte(`[{"name": "self", "path": "Files",
	   "target": {"type": "OneLake", "oneLake": {"workspaceId": "` + zero + `", "itemId": "` + zero + `", "path": "Files/f"}}}]`)
	out, outcome := rb.RebindPart(LocalItem{Type: "Lakehouse", DisplayName: "LH"}, "shortcuts.metadata.json", shortcuts, "test-data")
	if string(out) != string(shortcuts) || len(outcome.Changes) != 0 {
		t.Fatalf("self-reference shortcut must be untouched by the zero-workspace pass:\n%s\n%#v", out, outcome.Changes)
	}
}
