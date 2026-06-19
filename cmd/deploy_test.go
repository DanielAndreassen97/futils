package cmd

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
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
	items      map[string][]fabric.Item      // workspaceID -> items
	created    []string                      // displayNames created
	createdWS  map[string]string             // displayName -> workspaceID
	defByID    map[string]*fabric.Definition // itemID -> deployed definition (compare tests)
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
	if d, ok := f.defByID[id]; ok {
		return d, nil
	}
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
func (f *deployFakeAPI) UpdateItem(token, ws, id, displayName, description string) error { return nil }
func (f *deployFakeAPI) RebindReport(token, ws, reportID, datasetID string) error        { return nil }
func (f *deployFakeAPI) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	return "", "", nil
}

// platformDef builds a deployed definition for a notebook: a matching content
// part plus a .platform part carrying the given description — mirroring what
// Fabric's getDefinition returns.
func platformDef(content, description string) *fabric.Definition {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	platform := `{"metadata":{"type":"Notebook","displayName":"NB_A","description":"` + description + `"}}`
	return &fabric.Definition{Parts: []fabric.DefinitionPart{
		{Path: "notebook-content.py", Payload: enc(content), PayloadType: "InlineBase64"},
		{Path: ".platform", Payload: enc(platform), PayloadType: "InlineBase64"},
	}}
}

func TestDiffExistingRows_DescriptionDriftIsChanged(t *testing.T) {
	local := []deploy.LocalItem{{
		Type: "Notebook", DisplayName: "NB_A", Description: "Real desc",
		Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}},
	}}
	deployed := []fabric.Item{{ID: "nb-a-id", DisplayName: "NB_A", Type: "Notebook", WorkspaceID: "ws-1"}}
	rows := deploy.Compare(local, deployed, deployItemScope)
	fake := &deployFakeAPI{defByID: map[string]*fabric.Definition{
		"nb-a-id": platformDef("x=1", "Old desc"), // content matches, description differs
	}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, "TEST", deploy.Parameters{}, rows, nil)

	if rows[0].Class != deploy.ClassChanged {
		t.Fatalf("description drift must make the row Changed, got %v", rows[0].Class)
	}
	if len(diffs) != 1 {
		t.Fatalf("want 1 item diff, got %d", len(diffs))
	}
	var foundDesc bool
	for _, p := range diffs[0].Parts {
		if p.Old == "Old desc" && p.New == "Real desc" {
			foundDesc = true
		}
	}
	if !foundDesc {
		t.Errorf("description change not surfaced as a part diff: %+v", diffs[0].Parts)
	}
}

func TestDiffExistingRows_PlatformOnlyIsUnchanged(t *testing.T) {
	local := []deploy.LocalItem{{
		Type: "Notebook", DisplayName: "NB_A", Description: "Same",
		Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}},
	}}
	deployed := []fabric.Item{{ID: "nb-a-id", DisplayName: "NB_A", Type: "Notebook", WorkspaceID: "ws-1"}}
	rows := deploy.Compare(local, deployed, deployItemScope)
	fake := &deployFakeAPI{defByID: map[string]*fabric.Definition{
		"nb-a-id": platformDef("x=1", "Same"), // content + description both match
	}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, "TEST", deploy.Parameters{}, rows, nil)

	if rows[0].Class != deploy.ClassUnchanged {
		t.Fatalf("matching content + description must be Unchanged (no phantom .platform diff), got %v", rows[0].Class)
	}
	if len(diffs) != 0 {
		t.Errorf("want no diffs, got %+v", diffs)
	}
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

func TestPrintRebindSummaryDedupesByValue(t *testing.T) {
	groups := []deployGroup{
		{Changes: []deploy.RebindChange{
			{Kind: "Lakehouse", Old: "dev-lh", New: "test-lh"},
			{Kind: "Workspace", Old: "dev-ws", New: "test-ws"},
		}},
		{Changes: []deploy.RebindChange{
			{Kind: "Lakehouse", Old: "dev-lh", New: "test-lh"}, // duplicate across groups
		}},
	}
	out := captureStdout(t, func() { printRebindSummary(groups) })
	if strings.Count(out, "dev-lh") != 1 {
		t.Errorf("expected the duplicate Lakehouse change to appear once, got:\n%s", out)
	}
	if !strings.Contains(out, "test-ws") || !strings.Contains(out, "Workspace") {
		t.Errorf("workspace change missing:\n%s", out)
	}
	if !strings.Contains(out, "→") && !strings.Contains(out, "changes to") {
		t.Errorf("expected a change arrow/text:\n%s", out)
	}
}

func TestPrintRebindSummarySilentWhenNoChanges(t *testing.T) {
	out := captureStdout(t, func() { printRebindSummary([]deployGroup{{}}) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no output when nothing changed, got:\n%s", out)
	}
}

func TestFilterIgnoredUnresolvedDropsIgnored(t *testing.T) {
	groups := []deployGroup{{
		Unresolved: []deploy.UnresolvedRef{
			{GUID: "keep-1", ItemType: "Lakehouse"},
			{GUID: "drop-1", ItemType: "Lakehouse"},
		},
	}}
	customer := config.Customer{IgnoredReferences: []string{"drop-1"}}
	filterIgnoredUnresolved(groups, customer)
	if len(groups[0].Unresolved) != 1 || groups[0].Unresolved[0].GUID != "keep-1" {
		t.Fatalf("after filter = %#v", groups[0].Unresolved)
	}
}

func TestCountByClass(t *testing.T) {
	groups := []deployGroup{
		{Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew}, {Class: deploy.ClassChanged}, {Class: deploy.ClassChanged},
		}},
		{Rows: []deploy.CompareRow{
			{Class: deploy.ClassUnchanged}, {Class: deploy.ClassOrphan}, {Class: deploy.ClassChanged},
		}},
	}
	c := countByClass(groups)
	if c[deploy.ClassChanged] != 3 || c[deploy.ClassNew] != 1 || c[deploy.ClassUnchanged] != 1 || c[deploy.ClassOrphan] != 1 {
		t.Fatalf("counts = %#v", c)
	}
}
