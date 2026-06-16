package cmd

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// deployFakeAPI implements deploy.FabricClient for runDeploy tests.
type deployFakeAPI struct {
	workspaces []fabric.Workspace
	items      map[string][]fabric.Item // workspaceID -> items
	created    []string                 // displayNames created
}

func (f *deployFakeAPI) ListWorkspaces(token string) ([]fabric.Workspace, error) {
	return f.workspaces, nil
}
func (f *deployFakeAPI) ListItems(token, ws string) ([]fabric.Item, error) {
	return f.items[ws], nil
}
func (f *deployFakeAPI) ListItemsByType(token, ws, typ string) ([]fabric.Item, error) {
	var out []fabric.Item
	for _, it := range f.items[ws] {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *deployFakeAPI) GetItemDefinition(token, ws, id, format string) (*fabric.Definition, error) {
	return &fabric.Definition{}, nil
}
func (f *deployFakeAPI) CreateItem(token, ws, name, typ string, def *fabric.Definition) (fabric.Item, error) {
	f.created = append(f.created, name)
	return fabric.Item{ID: name + "-id", DisplayName: name, Type: typ, WorkspaceID: ws}, nil
}
func (f *deployFakeAPI) UpdateItemDefinition(token, ws, id string, def *fabric.Definition) error {
	return nil
}
func (f *deployFakeAPI) RebindReport(token, ws, reportID, datasetID string) error { return nil }
func (f *deployFakeAPI) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	return "", "", nil
}

func TestRunDeployHappyPath(t *testing.T) {
	local := []deploy.LocalItem{
		{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}
	fake := &deployFakeAPI{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		items:      map[string][]fabric.Item{"ws-test": {}},
	}
	// Pre-select all rows.
	selectRows := func(rows []deploy.CompareRow) ([]deploy.CompareRow, error) { return rows, nil }

	res, err := runDeploy(fake, "tok", fabric.Workspace{ID: "ws-test", DisplayName: "TEST"},
		"TEST", local, deploy.Parameters{}, selectRows, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("results: %+v", res)
	}
	if len(fake.created) != 1 {
		t.Errorf("expected 1 create, got %d", len(fake.created))
	}
}
