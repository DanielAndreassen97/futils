package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// deployFakeAPI implements deploy.FabricClient for runDeploy tests.
type deployFakeAPI struct {
	workspaces []fabric.Workspace
	items      map[string][]fabric.Item // workspaceID -> items
	created    []string                 // displayNames created
	createdWS  map[string]string        // displayName -> workspaceID
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
	if f.createdWS == nil {
		f.createdWS = map[string]string{}
	}
	f.createdWS[name] = ws
	return fabric.Item{ID: name + "-id", DisplayName: name, Type: typ, WorkspaceID: ws}, nil
}
func (f *deployFakeAPI) UpdateItemDefinition(token, ws, id string, def *fabric.Definition) error {
	return nil
}
func (f *deployFakeAPI) RebindReport(token, ws, reportID, datasetID string) error { return nil }
func (f *deployFakeAPI) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	return "", "", nil
}

func makeGroup(folder, wsID, wsName string, local []deploy.LocalItem, deployed []fabric.Item) deployGroup {
	return deployGroup{
		Folder:   folder,
		Target:   fabric.Workspace{ID: wsID, DisplayName: wsName},
		Rows:     deploy.Compare(local, deployed, deployItemScope),
		Deployed: deployed,
	}
}

func TestRunDeployHappyPath(t *testing.T) {
	fake := &deployFakeAPI{}
	local := []deploy.LocalItem{
		{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}
	groups := []deployGroup{makeGroup("Backend", "ws-1", "Config", local, nil)}
	selectAll := func(gs []deployGroup) (map[int][]deploy.LocalItem, error) {
		out := map[int][]deploy.LocalItem{}
		for i, g := range gs {
			for _, r := range g.Rows {
				if r.Class != deploy.ClassOrphan {
					out[i] = append(out[i], r.Local)
				}
			}
		}
		return out, nil
	}
	res, err := runDeploy(fake, "tok", "", groups, false, nil, selectAll, func(string) (bool, error) { return true, nil })
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

func TestRunDeployDryRunDoesNotExecute(t *testing.T) {
	fake := &deployFakeAPI{}
	local := []deploy.LocalItem{{Type: "Notebook", DisplayName: "NB_A",
		Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}}}}
	groups := []deployGroup{makeGroup("Backend", "ws-1", "Config", local, nil)}
	mustNotCall := func([]deployGroup) (map[int][]deploy.LocalItem, error) {
		t.Fatal("selectItems must not be called in dry-run")
		return nil, nil
	}
	res, err := runDeploy(fake, "tok", "", groups, true, nil, mustNotCall, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if res != nil {
		t.Errorf("dry-run should return nil results, got %+v", res)
	}
	if len(fake.created) != 0 {
		t.Errorf("dry-run must not create, got %d", len(fake.created))
	}
}

func TestRunDeployTwoGroupsDeployToOwnWorkspaces(t *testing.T) {
	fake := &deployFakeAPI{}
	backend := []deploy.LocalItem{{Type: "Notebook", DisplayName: "NB_A",
		Parts: []deploy.Part{{Path: "c.py", Content: []byte("x=1")}}}}
	frontend := []deploy.LocalItem{{Type: "Report", DisplayName: "R_A",
		Parts: []deploy.Part{{Path: "definition.pbir", Content: []byte("{}")}}}}
	groups := []deployGroup{
		makeGroup("Backend", "ws-config", "Config", backend, nil),
		makeGroup("Frontend", "ws-semmod", "SemMod", frontend, nil),
	}
	selectAll := func(gs []deployGroup) (map[int][]deploy.LocalItem, error) {
		out := map[int][]deploy.LocalItem{}
		for i, g := range gs {
			for _, r := range g.Rows {
				out[i] = append(out[i], r.Local)
			}
		}
		return out, nil
	}
	res, err := runDeploy(fake, "tok", "", groups, false, nil, selectAll, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
	if fake.createdWS["NB_A"] != "ws-config" || fake.createdWS["R_A"] != "ws-semmod" {
		t.Errorf("wrong target workspaces: %v", fake.createdWS)
	}
}

func TestPrintUnresolvedListsRefs(t *testing.T) {
	groups := []deployGroup{{
		Folder: "Backend",
		Target: fabric.Workspace{DisplayName: "DP - TEST - Config"},
		Unresolved: []deploy.UnresolvedRef{
			{GUID: "09bc360d-aaaa-bbbb-cccc-ddddeeeeffff", ItemType: "Lakehouse", Location: "known_lakehouses", ItemName: "NB_Config"},
		},
	}}
	out := captureStdout(t, func() { printUnresolved(groups) })
	if !strings.Contains(out, "NB_Config") || !strings.Contains(out, "09bc360d") {
		t.Errorf("unresolved output missing context:\n%s", out)
	}
	if !strings.Contains(out, "Lakehouse") {
		t.Errorf("unresolved output missing type guess:\n%s", out)
	}
}

func TestPrintUnresolvedSilentWhenNone(t *testing.T) {
	out := captureStdout(t, func() { printUnresolved([]deployGroup{{Folder: "Backend"}}) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no output when nothing unresolved, got:\n%s", out)
	}
}
