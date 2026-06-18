package deploy

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// recordingFabric extends fakeFabric (from resolve_test.go) to capture
// create/update/rebind calls.
type recordingFabric struct {
	fakeFabric
	created      []fabric.Definition
	createdNames []string
	updates      map[string]fabric.Definition // existingID -> def
	rebinds      [][2]string                  // {reportID, datasetID}
}

func (r *recordingFabric) CreateItem(token, ws, name, typ string, def *fabric.Definition) (fabric.Item, error) {
	r.created = append(r.created, *def)
	r.createdNames = append(r.createdNames, name)
	return fabric.Item{ID: name + "-newid", DisplayName: name, Type: typ, WorkspaceID: ws}, nil
}
func (r *recordingFabric) UpdateItemDefinition(token, ws, id string, def *fabric.Definition) error {
	if r.updates == nil {
		r.updates = map[string]fabric.Definition{}
	}
	r.updates[id] = *def
	return nil
}
func (r *recordingFabric) RebindReport(token, ws, reportID, datasetID string) error {
	r.rebinds = append(r.rebinds, [2]string{reportID, datasetID})
	return nil
}

func decodePart(t *testing.T, def fabric.Definition, suffix string) string {
	t.Helper()
	for _, p := range def.Parts {
		if strings.HasSuffix(p.Path, suffix) {
			b, err := base64.StdEncoding.DecodeString(p.Payload)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			return string(b)
		}
	}
	t.Fatalf("no part ending %q", suffix)
	return ""
}

func TestExecuteAppliesParametersAndEncodes(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	params := Parameters{FindReplace: []FindReplace{{
		FindValue:    "DEV-GUID",
		ReplaceValue: map[string]string{"_ALL_": "TEST-GUID"},
	}}}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{
			Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid-nb",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("id=DEV-GUID")}},
		},
	}}

	res, err := Execute(rf, "tok", target, "TEST", plan, params, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("result: %+v", res)
	}
	if got := decodePart(t, rf.created[0], "notebook-content.py"); got != "id=TEST-GUID" {
		t.Errorf("substitution not applied: %q", got)
	}
}

func TestExecuteUpdatesExistingItem(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action:     ActionUpdate,
		ExistingID: "existing-id",
		Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}}
	res, err := Execute(rf, "tok", target, "TEST", plan, Parameters{}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("result: %+v", res)
	}
	if res[0].Action != ActionUpdate || res[0].ID != "existing-id" {
		t.Errorf("expected update of existing-id, got %+v", res[0])
	}
	if len(rf.created) != 0 {
		t.Errorf("update path must not create, got %d creates", len(rf.created))
	}
	if _, ok := rf.updates["existing-id"]; !ok {
		t.Errorf("definition was not pushed to existing-id; updates=%v", rf.updates)
	}
}

func TestExecuteRebindReportToModelInSameRun(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}

	model := LocalItem{Type: "SemanticModel", DisplayName: "MyModel", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	pbir := `{"datasetReference":{"byPath":{"path":"../MyModel.SemanticModel"}}}`
	report := LocalItem{Type: "Report", DisplayName: "MyReport", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: []byte(pbir)}}}

	plan := BuildPlan([]LocalItem{report, model}, nil) // ordered: model then report
	res, err := Execute(rf, "tok", target, "TEST", plan, Parameters{}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("item %s failed: %v", r.Name, r.Err)
		}
	}
	if len(rf.rebinds) != 1 {
		t.Fatalf("want 1 rebind, got %d", len(rf.rebinds))
	}
	if rf.rebinds[0][1] != "MyModel-newid" {
		t.Errorf("rebind dataset = %q, want MyModel-newid", rf.rebinds[0][1])
	}
}
