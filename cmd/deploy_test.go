package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	rebinds    [][3]string                   // {workspaceID, reportID, datasetID}
	sqlByLH    map[string][2]string          // lakehouseID -> {host, id} (endpoint tests)
	bulkWS     []string                      // workspace IDs passed to BulkImportDefinitions
	bulkParts  [][]fabric.DefinitionPart     // parts passed per bulk call
	bulkOpts   []fabric.BulkImportOptions
	bulkResult *fabric.BulkImportResult // returned from each bulk call (nil → empty)

	envPublished  []string            // itemIDs passed to PublishEnvironment
	envPublishErr error               // when set, PublishEnvironment fails
	envStates     map[string][]string // itemID -> publish-state sequence (last repeats)
	envStatePolls int                 // GetEnvironmentPublishState call count
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
func (f *deployFakeAPI) CreateItem(token, ws, name, typ string, def *fabric.Definition, creationPayload json.RawMessage) (fabric.Item, error) {
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
func (f *deployFakeAPI) DeleteItem(token, ws, id string) error                           { return nil }
func (f *deployFakeAPI) RebindReport(token, ws, reportID, datasetID string) error {
	f.rebinds = append(f.rebinds, [3]string{ws, reportID, datasetID})
	return nil
}
func (f *deployFakeAPI) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	v := f.sqlByLH[lhID]
	return v[0], v[1], nil
}
func (f *deployFakeAPI) BulkImportDefinitions(token, ws string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error) {
	f.bulkWS = append(f.bulkWS, ws)
	f.bulkParts = append(f.bulkParts, parts)
	f.bulkOpts = append(f.bulkOpts, opts)
	if f.bulkResult != nil {
		return f.bulkResult, nil
	}
	return &fabric.BulkImportResult{}, nil
}

// APIClient stubs not exercised by deploy tests — present so deployFakeAPI can
// be handed to buildDeployGroups, which takes the full APIClient.
func (f *deployFakeAPI) GetAccessToken(string) (string, error)         { return "tok", nil }
func (f *deployFakeAPI) GetWorkspaceID(_, name string) (string, error) { return name, nil }
func (f *deployFakeAPI) ListNotebooks(_, ws string) ([]fabric.Item, error) {
	return f.ListItemsByType("", ws, "Notebook")
}
func (f *deployFakeAPI) GetNotebookIpynb(string, string, string) ([]byte, error) {
	return nil, fmt.Errorf("not used by deploy tests")
}
func (f *deployFakeAPI) RunNotebook(string, string, string, []fabric.JobInput, *fabric.DefaultLakehouse) (string, error) {
	return "", fmt.Errorf("not used by deploy tests")
}
func (f *deployFakeAPI) GetJobInstance(string, string) (fabric.JobInstanceStatus, error) {
	return fabric.JobInstanceStatus{}, fmt.Errorf("not used by deploy tests")
}
func (f *deployFakeAPI) ListDatasets(string, string) ([]fabric.Dataset, error) { return nil, nil }
func (f *deployFakeAPI) QueryRefreshableTables(string, string, string) ([]string, error) {
	return nil, nil
}
func (f *deployFakeAPI) TriggerRefresh(string, string, string, []string) (string, error) {
	return "", fmt.Errorf("not used by deploy tests")
}
func (f *deployFakeAPI) WaitForRefresh(string, string, string, string) (fabric.RefreshStatus, error) {
	return fabric.RefreshStatus{}, fmt.Errorf("not used by deploy tests")
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
	rows := deploy.Compare(local, deployed, localTypeScope(local))
	fake := &deployFakeAPI{defByID: map[string]*fabric.Definition{
		"nb-a-id": platformDef("x=1", "Old desc"), // content matches, description differs
	}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, rows, nil, false)

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
	rows := deploy.Compare(local, deployed, localTypeScope(local))
	fake := &deployFakeAPI{defByID: map[string]*fabric.Definition{
		"nb-a-id": platformDef("x=1", "Same"), // content + description both match
	}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, rows, nil, false)

	if rows[0].Class != deploy.ClassUnchanged {
		t.Fatalf("matching content + description must be Unchanged (no phantom .platform diff), got %v", rows[0].Class)
	}
	if len(diffs) != 0 {
		t.Errorf("want no diffs, got %+v", diffs)
	}
}

// Zero-part items (Warehouse and other shell types) have no definition to
// fetch — getDefinition is unsupported for them — so the compare must classify
// them on description alone, never leaving them as unverified Exists.
func TestDiffExistingRows_ZeroPartShellTypes(t *testing.T) {
	local := []deploy.LocalItem{
		{Type: "Warehouse", DisplayName: "WH_Same", Description: "d"},
		{Type: "Warehouse", DisplayName: "WH_Drift", Description: "new desc"},
	}
	deployed := []fabric.Item{
		{ID: "wh-1", DisplayName: "WH_Same", Type: "Warehouse", WorkspaceID: "ws-1", Description: "d"},
		{ID: "wh-2", DisplayName: "WH_Drift", Type: "Warehouse", WorkspaceID: "ws-1", Description: "old desc"},
	}
	rows := deploy.Compare(local, deployed, localTypeScope(local))
	fake := &deployFakeAPI{} // no definitions — a fetch attempt would return an empty def, not an error, so also assert none happens via class outcome
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, rows, nil, false)

	byName := map[string]deploy.Class{}
	for _, r := range rows {
		byName[r.Name()] = r.Class
	}
	if byName["WH_Same"] != deploy.ClassUnchanged {
		t.Errorf("WH_Same = %v, want Unchanged", byName["WH_Same"])
	}
	if byName["WH_Drift"] != deploy.ClassChanged {
		t.Errorf("WH_Drift = %v, want Changed", byName["WH_Drift"])
	}
	if len(diffs) != 1 || diffs[0].Name != "WH_Drift" || diffs[0].Parts[0].New != "new desc" {
		t.Errorf("want one description diff for WH_Drift, got %+v", diffs)
	}
}

// simpleDef builds a deployed definition with a single content part (no .platform).
func simpleDef(path, content string) *fabric.Definition {
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	return &fabric.Definition{Parts: []fabric.DefinitionPart{
		{Path: path, Payload: enc, PayloadType: "InlineBase64"},
	}}
}

// TestDiffExistingRows_ClassNewDepMakesRefChanged verifies that an Exists item R
// whose local content references a ClassNew dependency D's logicalId is reported
// as ClassChanged (not ClassUnchanged) — because publish will substitute a fresh
// GUID for D's logicalId, mutating R even if the deployed copy looks identical.
// An unrelated Exists item that does NOT reference D must remain ClassUnchanged.
func TestDiffExistingRows_ClassNewDepMakesRefChanged(t *testing.T) {
	const depLogicalID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	// D: ClassNew item — will be created fresh during publish.
	depLocal := deploy.LocalItem{
		Type: "SemanticModel", DisplayName: "Model_D", LogicalID: depLogicalID,
		Parts: []deploy.Part{{Path: "model.bim", Content: []byte(`{"name":"Model_D"}`)}},
	}

	// R: ClassExists item whose local part content embeds D's logicalId.
	// The deployed copy also contains the literal logicalId (pre-fix normalizes equal → Unchanged).
	// Description is intentionally empty on both sides so only content drives the verdict.
	const rContent = `{"datasetId":"` + depLogicalID + `","extra":"val"}`
	refLocal := deploy.LocalItem{
		Type: "Report", DisplayName: "Report_R", LogicalID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Parts: []deploy.Part{{Path: "report.json", Content: []byte(rContent)}},
	}

	// U: ClassExists item whose content does NOT reference depLogicalID.
	// Description is intentionally empty on both sides so only content drives the verdict.
	unrelLocal := deploy.LocalItem{
		Type: "Notebook", DisplayName: "NB_U", LogicalID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
		Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}},
	}

	deployedItems := []fabric.Item{
		{ID: "report-r-id", DisplayName: "Report_R", Type: "Report", WorkspaceID: "ws-1"},
		{ID: "nb-u-id", DisplayName: "NB_U", Type: "Notebook", WorkspaceID: "ws-1"},
	}
	localItems := []deploy.LocalItem{depLocal, refLocal, unrelLocal}
	rows := deploy.Compare(localItems, deployedItems, localTypeScope(localItems))

	fake := &deployFakeAPI{defByID: map[string]*fabric.Definition{
		// R's deployed definition contains the literal logicalId (same as local) → would be Unchanged without the fix.
		"report-r-id": simpleDef("report.json", rContent),
		// U's deployed definition matches its local content exactly (no description drift either).
		"nb-u-id": simpleDef("notebook-content.py", "x=1"),
	}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}

	_, _, diffs := diffExistingRows(fake, "tok", target, rows, nil, false)

	// Find rows by display name for clear assertions.
	rowByName := map[string]*deploy.CompareRow{}
	for i := range rows {
		rowByName[rows[i].Name()] = &rows[i]
	}

	rRow := rowByName["Report_R"]
	if rRow == nil {
		t.Fatal("Report_R row not found")
	}
	if rRow.Class != deploy.ClassChanged {
		t.Errorf("Report_R references a ClassNew dep — must be ClassChanged, got %v", rRow.Class)
	}

	var foundR bool
	for _, d := range diffs {
		if d.Name == "Report_R" {
			foundR = true
			// The diff's new side should contain the sentinel (not the logicalId).
			var sentinelFound bool
			for _, p := range d.Parts {
				if strings.Contains(p.New, "futils:pending-new-item:") {
					sentinelFound = true
				}
			}
			if !sentinelFound {
				t.Errorf("Report_R diff should show sentinel in New side, got parts: %+v", d.Parts)
			}
		}
	}
	if !foundR {
		t.Error("Report_R should appear in itemDiffs")
	}

	// Guard: unrelated item must still be Unchanged (no false positive).
	uRow := rowByName["NB_U"]
	if uRow == nil {
		t.Fatal("NB_U row not found")
	}
	if uRow.Class != deploy.ClassUnchanged {
		t.Errorf("NB_U does not reference the ClassNew dep — must stay ClassUnchanged, got %v", uRow.Class)
	}
}

// TestDiffExistingRows_MultiItemDeterministicOrder is the determinism guard for
// the parallelized compare (#9). It drives a handful of Exists rows mixing
// changed / unchanged / unverified items and asserts the merged outputs
// (reclassifications + itemDiffs/changes/unresolved AND their order) match what
// the old serial existsIdx-ordered loop produced. The parallel pool must not
// reorder anything; only wall-clock changes.
func TestDiffExistingRows_MultiItemDeterministicOrder(t *testing.T) {
	// Five notebooks, fixed local order. A custom substitution rewrites the
	// literal "DEV-HOST" -> the target lakehouse's GUID by name, so every
	// "changed" item emits a deterministic RebindChange{Kind:"Substitution"}.
	mk := func(name, content string) deploy.LocalItem {
		return deploy.LocalItem{
			Type: "Notebook", DisplayName: name,
			Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte(content)}},
		}
	}
	local := []deploy.LocalItem{
		mk("NB_1", "host=DEV-HOST # one"),   // changed (sub rewrites + content drift)
		mk("NB_2", "x=2"),                   // unchanged (deployed matches)
		mk("NB_3", "host=DEV-HOST # three"), // changed
		mk("NB_4", "x=4-unverified"),        // unverified (no deployed def -> fetch returns empty, content differs => actually changed)
		mk("NB_5", "host=DEV-HOST # five"),  // changed
	}
	deployed := []fabric.Item{
		{ID: "nb1", DisplayName: "NB_1", Type: "Notebook", WorkspaceID: "ws-1"},
		{ID: "nb2", DisplayName: "NB_2", Type: "Notebook", WorkspaceID: "ws-1"},
		{ID: "nb3", DisplayName: "NB_3", Type: "Notebook", WorkspaceID: "ws-1"},
		{ID: "nb4", DisplayName: "NB_4", Type: "Notebook", WorkspaceID: "ws-1"},
		{ID: "nb5", DisplayName: "NB_5", Type: "Notebook", WorkspaceID: "ws-1"},
	}
	rows := deploy.Compare(local, deployed, localTypeScope(local))

	// Deployed definitions: NB_2 matches exactly (unchanged); the rest differ.
	fake := &deployFakeAPI{
		workspaces: []fabric.Workspace{{ID: "ws-1", DisplayName: "Config"}},
		items:      map[string][]fabric.Item{"ws-1": {{ID: "tgt-lh", DisplayName: "LH_Target", Type: "Lakehouse"}}},
		defByID: map[string]*fabric.Definition{
			"nb1": simpleDef("notebook-content.py", "host=OLD # one"),
			"nb2": simpleDef("notebook-content.py", "x=2"),
			"nb3": simpleDef("notebook-content.py", "host=OLD # three"),
			"nb4": simpleDef("notebook-content.py", "host=OLD # four"),
			"nb5": simpleDef("notebook-content.py", "host=OLD # five"),
		},
	}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}
	ws := []fabric.Workspace{target}
	rb, err := deploy.NewRebinder(fake, "tok", ws, ws, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	rb.SetSubstitutions([]deploy.Substitution{{
		FindValue:  "DEV-HOST",
		TargetType: "Lakehouse",
		TargetName: "LH_Target",
		Attr:       "id",
	}})

	unresolved, changes, diffs := diffExistingRows(fake, "tok", target, rows, rb, false)

	// itemDiffs must be in existsIdx (== local) order: NB_1, NB_3, NB_4, NB_5.
	wantDiffOrder := []string{"NB_1", "NB_3", "NB_4", "NB_5"}
	var gotDiffOrder []string
	for _, d := range diffs {
		gotDiffOrder = append(gotDiffOrder, d.Name)
	}
	if strings.Join(gotDiffOrder, ",") != strings.Join(wantDiffOrder, ",") {
		t.Fatalf("itemDiffs order = %v, want %v", gotDiffOrder, wantDiffOrder)
	}
	// NB_2 unchanged, the rest changed.
	byName := map[string]deploy.Class{}
	for i := range rows {
		byName[rows[i].Name()] = rows[i].Class
	}
	for _, n := range []string{"NB_1", "NB_3", "NB_4", "NB_5"} {
		if byName[n] != deploy.ClassChanged {
			t.Errorf("%s want Changed, got %v", n, byName[n])
		}
	}
	if byName["NB_2"] != deploy.ClassUnchanged {
		t.Errorf("NB_2 want Unchanged, got %v", byName["NB_2"])
	}
	// The substitution fires for NB_1/NB_3/NB_5 (DEV-HOST present) — one
	// RebindChange each, in existsIdx order. NB_4 has no DEV-HOST, no change.
	if len(changes) != 3 {
		t.Fatalf("want 3 substitution changes, got %d: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.Old != "DEV-HOST" || c.New != "tgt-lh" || c.Kind != "Substitution" {
			t.Errorf("unexpected change: %+v", c)
		}
	}
	if len(unresolved) != 0 {
		t.Errorf("want no unresolved, got %+v", unresolved)
	}
}

// TestDiffExistingRows_RaceSharedCaches drives diffExistingRows with many Exists
// items that all resolve the SAME target lakehouse via a custom substitution, so
// the internally-created Resolver and the shared Rebinder have their lazy caches
// populated by the parallel compare workers at once. Run under `go test -race`:
// must be clean. Without the Resolver/Rebinder mutexes this trips the detector.
func TestDiffExistingRows_RaceSharedCaches(t *testing.T) {
	const n = 24
	var local []deploy.LocalItem
	var deployed []fabric.Item
	defByID := map[string]*fabric.Definition{}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("NB_%02d", i)
		id := fmt.Sprintf("id-%02d", i)
		local = append(local, deploy.LocalItem{
			Type: "Notebook", DisplayName: name,
			// "$ENDPOINT$" -> target lakehouse SQL endpoint host (hits the
			// Rebinder targetEndpointFor cache); content differs from deployed.
			Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("ep=$ENDPOINT$")}},
		})
		deployed = append(deployed, fabric.Item{ID: id, DisplayName: name, Type: "Notebook", WorkspaceID: "ws-1"})
		defByID[id] = simpleDef("notebook-content.py", "ep=OLD")
	}
	rows := deploy.Compare(local, deployed, localTypeScope(local))

	fake := &deployFakeAPI{
		workspaces: []fabric.Workspace{{ID: "ws-1", DisplayName: "Config"}},
		items:      map[string][]fabric.Item{"ws-1": {{ID: "tgt-lh", DisplayName: "LH_Target", Type: "Lakehouse"}}},
		defByID:    defByID,
		sqlByLH:    map[string][2]string{"tgt-lh": {"target.datawarehouse.fabric.microsoft.com", "tgt-ep"}},
	}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Config"}
	ws := []fabric.Workspace{target}
	rb, err := deploy.NewRebinder(fake, "tok", ws, ws, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	// Every item resolves the SAME lakehouse endpoint -> contended cache fill.
	rb.SetSubstitutions([]deploy.Substitution{{
		FindValue:  "$ENDPOINT$",
		TargetType: "Lakehouse",
		TargetName: "LH_Target",
		Attr:       "sqlendpoint",
	}})

	_, changes, diffs := diffExistingRows(fake, "tok", target, rows, rb, false)

	if len(diffs) != n {
		t.Fatalf("want %d changed items, got %d", n, len(diffs))
	}
	// One substitution change recorded per item, all rewriting to the resolved
	// target endpoint host. Order must follow existsIdx (NB_00..NB_23).
	if len(changes) != n {
		t.Fatalf("want %d changes, got %d", n, len(changes))
	}
	for i, c := range changes {
		if c.New != "target.datawarehouse.fabric.microsoft.com" {
			t.Fatalf("change %d resolved to %q, want target endpoint host", i, c.New)
		}
	}
	for i, d := range diffs {
		if want := fmt.Sprintf("NB_%02d", i); d.Name != want {
			t.Fatalf("diff %d = %q, want %q (order not preserved)", i, d.Name, want)
		}
	}
}

func TestExcludedSet(t *testing.T) {
	if s := excludedSet(config.Customer{}); len(s) != 0 {
		t.Errorf("default = nothing excluded, got %#v", s)
	}
	s := excludedSet(config.Customer{ExcludedItemTypes: []string{"Lakehouse"}})
	if !s["Lakehouse"] || s["Notebook"] {
		t.Errorf("only Lakehouse excluded, got %#v", s)
	}
}

func TestFilterExcludedTypes(t *testing.T) {
	items := []deploy.LocalItem{
		{Type: "Notebook", DisplayName: "NB"},
		{Type: "Lakehouse", DisplayName: "LH"},
	}
	got := filterExcludedTypes(items, map[string]bool{"Lakehouse": true})
	if len(got) != 1 || got[0].DisplayName != "NB" {
		t.Errorf("expected only NB kept, got %#v", got)
	}
}

func TestLocalTypeScope(t *testing.T) {
	items := []deploy.LocalItem{{Type: "Notebook"}, {Type: "Notebook"}, {Type: "Report"}}
	s := localTypeScope(items)
	if !s["Notebook"] || !s["Report"] || len(s) != 2 {
		t.Errorf("scope = %#v, want {Notebook,Report}", s)
	}
}

func TestPrintGroupedCompareHidesUnchanged(t *testing.T) {
	rows := []deploy.CompareRow{
		{Class: deploy.ClassChanged, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Changed"}},
		{Class: deploy.ClassUnchanged, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Unchanged"}},
	}
	groups := []deployGroup{{Folder: "F", Target: fabric.Workspace{DisplayName: "WS"}, Rows: rows}}
	out := captureStdout(t, func() { printGroupedCompare(groups) })

	if !strings.Contains(out, "NB_Changed") {
		t.Error("changed item should be listed")
	}
	if strings.Contains(out, "NB_Unchanged") {
		t.Error("unchanged item must NOT be listed in the per-row output")
	}
	if !strings.Contains(out, "Unchanged") {
		t.Error("the count summary should still report the Unchanged total")
	}
}

func TestPrintGroupedCompareSortsByClass(t *testing.T) {
	// Rows given out of class order; output must group New before Changed before
	// Orphan (the class word is dropped — color + legend convey it).
	rows := []deploy.CompareRow{
		{Class: deploy.ClassChanged, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Changed"}},
		{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_Orphan"}},
		{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_New"}},
	}
	groups := []deployGroup{{Folder: "F", Target: fabric.Workspace{DisplayName: "WS"}, Rows: rows}}
	out := captureStdout(t, func() { printGroupedCompare(groups) })

	// Only assert on the per-row region (after the legend, which names every class).
	body := out[strings.Index(out, "WS"):]
	iNew := strings.Index(body, "NB_New")
	iChanged := strings.Index(body, "NB_Changed")
	iOrphan := strings.Index(body, "NB_Orphan")
	if iNew < 0 || iChanged < 0 || iOrphan < 0 {
		t.Fatalf("missing a row: new=%d changed=%d orphan=%d\n%s", iNew, iChanged, iOrphan, body)
	}
	if !(iNew < iChanged && iChanged < iOrphan) {
		t.Errorf("rows not sorted New<Changed<Orphan: new=%d changed=%d orphan=%d", iNew, iChanged, iOrphan)
	}
}

func TestTargetsSummary(t *testing.T) {
	groups := []deployGroup{
		{Target: fabric.Workspace{DisplayName: "WS-A"}},
		{Target: fabric.Workspace{DisplayName: "WS-B"}},
		{Target: fabric.Workspace{DisplayName: "WS-A"}}, // duplicate collapses
	}
	if got := targetsSummary(groups); got != "WS-A, WS-B" {
		t.Errorf("targetsSummary = %q, want %q", got, "WS-A, WS-B")
	}
	if got := targetsSummary(nil); got != "(none)" {
		t.Errorf("empty targetsSummary = %q, want (none)", got)
	}
}

func TestSaveDeployHistoryWritesOnlyWhenDeployed(t *testing.T) {
	repo := t.TempDir()
	customer := config.Customer{RepoPath: repo, DeployHistoryPath: "history"}
	groups := []deployGroup{{Target: fabric.Workspace{DisplayName: "WS"}}}
	results := []deploy.Result{{Name: "NB", Type: "Notebook", Action: deploy.ActionCreate}}

	decline := func(string) (bool, error) { return false, nil }

	// Nothing published (empty results) → no report, no folder created.
	_ = captureStdout(t, func() { saveDeployHistory("", "acme", customer, groups, nil, nil, decline) })
	if entries, _ := os.ReadDir(filepath.Join(repo, "history")); len(entries) != 0 {
		t.Errorf("empty results must write no report, found %d file(s)", len(entries))
	}

	// Items deployed → a .html report appears in the configured folder.
	_ = captureStdout(t, func() { saveDeployHistory("", "acme", customer, groups, results, nil, decline) })
	entries, err := os.ReadDir(filepath.Join(repo, "history"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 report file, got %d (err %v)", len(entries), err)
	}
	if !strings.HasSuffix(entries[0].Name(), ".html") {
		t.Errorf("report should be .html, got %q", entries[0].Name())
	}

	// Deployed but history unconfigured → the offer declined → skip notice.
	out := captureStdout(t, func() {
		saveDeployHistory("", "acme", config.Customer{RepoPath: repo}, groups, results, nil, decline)
	})
	if !strings.Contains(out, "No deploy-history folder set") {
		t.Errorf("expected skip notice when unconfigured, got %q", out)
	}
}

// TestSaveDeployHistoryOffersSetupOnFirstDeploy: accepting the first-deploy
// offer persists docs/deploys to the customer config AND writes THIS run's
// report there.
func TestSaveDeployHistoryOffersSetupOnFirstDeploy(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	customer := config.Customer{RepoPath: repo}
	if err := config.Save(configPath, config.Config{Customers: map[string]config.Customer{"acme": customer}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	groups := []deployGroup{{Target: fabric.Workspace{DisplayName: "WS"}}}
	results := []deploy.Result{{Name: "NB", Type: "Notebook", Action: deploy.ActionCreate}}

	accept := func(string) (bool, error) { return true, nil }
	_ = captureStdout(t, func() {
		saveDeployHistory(configPath, "acme", customer, groups, results, nil, accept)
	})

	entries, err := os.ReadDir(filepath.Join(repo, "docs", "deploys"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 report in docs/deploys, got %d (err %v)", len(entries), err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := cfg.Customers["acme"].DeployHistoryPath; got != "docs/deploys" {
		t.Errorf("DeployHistoryPath = %q, want docs/deploys", got)
	}
}

func makeGroup(folder, wsID, wsName string, local []deploy.LocalItem, deployed []fabric.Item) deployGroup {
	return deployGroup{
		Folder:   folder,
		Target:   fabric.Workspace{ID: wsID, DisplayName: wsName},
		Rows:     deploy.Compare(local, deployed, localTypeScope(local)),
		Deployed: deployed,
	}
}

// selectAll is a test helper that selects every non-orphan item across all groups.
func selectAll(gs []deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
	out := map[int][]deploy.LocalItem{}
	for i, g := range gs {
		for _, r := range g.Rows {
			if r.Class != deploy.ClassOrphan {
				out[i] = append(out[i], r.Local)
			}
		}
	}
	return out, nil, nil
}

func TestRunDeployHappyPath(t *testing.T) {
	fake := &deployFakeAPI{}
	local := []deploy.LocalItem{
		{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Parts: []deploy.Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}
	groups := []deployGroup{makeGroup("Backend", "ws-1", "Config", local, nil)}
	res, err := runDeploy(fake, "tok", groups, selectAll, func(string) (bool, error) { return true, nil }, false)
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

func TestRunDeployDeclinedConfirmDoesNotExecute(t *testing.T) {
	fake := &deployFakeAPI{workspaces: []fabric.Workspace{{ID: "ws1", DisplayName: "WS"}}}
	groups := []deployGroup{makeGroup("F", "ws1", "WS",
		[]deploy.LocalItem{{Type: "Notebook", DisplayName: "NB", LogicalID: "lid"}}, nil)}

	res, err := runDeploy(fake, "tok", groups, selectAll, func(string) (bool, error) { return false, nil }, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("declined confirm must execute nothing, got %d results", len(res))
	}
	if len(fake.created) != 0 {
		t.Errorf("declined confirm must create nothing, got %d", len(fake.created))
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
	res, err := runDeploy(fake, "tok", groups, selectAll, func(string) (bool, error) { return true, nil }, false)
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

// TestRunDeployCrossGroupRebind proves the runDeploy wiring of fix #2: a model
// in one folder/group and its report in a SEPARATE group both mapping to the
// SAME workspace must end with the report rebound to the model's new GUID, even
// though they were two separate Execute calls.
func TestRunDeployCrossGroupRebind(t *testing.T) {
	fake := &deployFakeAPI{}
	model := []deploy.LocalItem{{Type: "SemanticModel", DisplayName: "MyModel", LogicalID: "lid-m",
		Parts: []deploy.Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}}
	report := []deploy.LocalItem{{Type: "Report", DisplayName: "MyReport", LogicalID: "lid-r",
		Parts: []deploy.Part{{Path: "definition.pbir",
			Content: []byte(`{"datasetReference":{"byPath":{"path":"../MyModel.SemanticModel"}}}`)}}}}
	// Report group listed FIRST — order must not matter.
	groups := []deployGroup{
		makeGroup("Frontend", "ws-shared", "Shared", report, nil),
		makeGroup("Backend", "ws-shared", "Backend", model, nil),
	}
	res, err := runDeploy(fake, "tok", groups, selectAll, func(string) (bool, error) { return true, nil }, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
	if len(fake.rebinds) != 1 {
		t.Fatalf("want exactly 1 rebind, got %d: %v", len(fake.rebinds), fake.rebinds)
	}
	if fake.rebinds[0][0] != "ws-shared" || fake.rebinds[0][2] != "MyModel-id" {
		t.Errorf("rebind = %v, want {ws-shared, MyReport-id, MyModel-id}", fake.rebinds[0])
	}
	// The report's Result must carry no error/warning (it rebound cleanly).
	for _, r := range res {
		if r.Name == "MyReport" && (r.Err != nil || r.Warning != "") {
			t.Errorf("report result should be clean, got err=%v warning=%q", r.Err, r.Warning)
		}
	}
}

// TestRunDeployByConnectionWarning proves the runDeploy wiring: a byConnection
// report deployed with a nil rebinder (no in-payload rewrite ran) and no same-run
// model must emit a warning so the operator knows to verify the binding manually.
func TestRunDeployByConnectionWarning(t *testing.T) {
	fake := &deployFakeAPI{}
	report := []deploy.LocalItem{{Type: "Report", DisplayName: "ConnReport", LogicalID: "lid-r",
		Parts: []deploy.Part{{Path: "definition.pbir",
			Content: []byte(`{"datasetReference":{"byConnection":{"connectionType":"pbiServiceXmlaStyleLive"}}}`)}}}}
	groups := []deployGroup{makeGroup("Frontend", "ws-1", "WS", report, nil)}
	res, err := runDeploy(fake, "tok", groups, selectAll, func(string) (bool, error) { return true, nil }, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(fake.rebinds) != 0 {
		t.Fatalf("byConnection report must not rebind (no model to bind to), got %v", fake.rebinds)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].Warning == "" || !strings.Contains(res[0].Warning, "byConnection") {
		t.Errorf("nil-rebinder byConnection report must warn, got warning=%q err=%v", res[0].Warning, res[0].Err)
	}
}

// newDeployGroupsRebinder builds a Rebinder over the fake's dev/target
// workspaces for buildDeployGroups tests.
func newDeployGroupsRebinder(t *testing.T, fake *deployFakeAPI) *deploy.Rebinder {
	t.Helper()
	rb, err := deploy.NewRebinder(fake, "tok",
		[]fabric.Workspace{{ID: "ws-dev", DisplayName: "DEV"}},
		[]fabric.Workspace{{ID: "ws-tgt", DisplayName: "TGT"}}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

// TestBuildDeployGroupsReportUnresolvedCarriesItemName proves finding-cluster
// fix: unresolved refs from the dedicated report-binding pass must carry the
// report's name (printUnresolved shows "<guid> in <ItemName>"), and a custom
// substitution whose target is missing must surface for a NEW report too.
func TestBuildDeployGroupsReportUnresolvedCarriesItemName(t *testing.T) {
	fake := &deployFakeAPI{
		workspaces: []fabric.Workspace{
			{ID: "ws-dev", DisplayName: "DEV"}, {ID: "ws-tgt", DisplayName: "TGT"},
		},
		items: map[string][]fabric.Item{"ws-dev": {}, "ws-tgt": {}},
	}
	rb := newDeployGroupsRebinder(t, fake)
	rb.SetSubstitutions([]deploy.Substitution{{
		ItemType: "Report", ItemName: "NewRep",
		FindValue: "placeholder", TargetType: "Lakehouse", TargetName: "LH_Missing",
	}})
	// Flat byConnection: model name "HR" resolves from the catalog but exists in
	// neither index → binding unresolved (not-in-target).
	report := deploy.LocalItem{Type: "Report", DisplayName: "NewRep", FolderPath: "Frontend",
		Parts: []deploy.Part{{Path: "definition.pbir",
			Content: []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DEV\";initial catalog=HR;semanticmodelid=99999999-9999-9999-9999-999999999999"}}}`)}}}

	customer := config.Customer{RepoPath: "/repo"}
	groups, err := buildDeployGroups(fake, "tok", customer,
		[]config.DeployMapping{{Folder: "Frontend", Workspace: "TGT"}},
		map[string][]deploy.LocalItem{customer.RepoPath: {report}}, fake.workspaces, &rebinderSet{shared: rb}, nil)
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	var binding, customSub int
	for _, u := range groups[0].Unresolved {
		if u.ItemName != "NewRep" {
			t.Errorf("unresolved ref %+v must carry ItemName=NewRep", u)
		}
		switch u.Location {
		case "report dataset binding":
			binding++
		case "custom substitution":
			customSub++
		}
	}
	if binding != 1 {
		t.Errorf("want exactly 1 binding unresolved, got %d (%+v)", binding, groups[0].Unresolved)
	}
	if customSub != 1 {
		t.Errorf("new report's custom-substitution unresolved must surface exactly once, got %d (%+v)", customSub, groups[0].Unresolved)
	}
}

// TestBuildDeployGroupsExistingReportCustomSubUnresolvedSurfaces proves that an
// EXISTING report's unresolved custom substitution is not silently dropped by
// the "binding pass owns report unresolved" filter in diffExistingRows.
func TestBuildDeployGroupsExistingReportCustomSubUnresolvedSurfaces(t *testing.T) {
	fake := &deployFakeAPI{
		workspaces: []fabric.Workspace{
			{ID: "ws-dev", DisplayName: "DEV"}, {ID: "ws-tgt", DisplayName: "TGT"},
		},
		items: map[string][]fabric.Item{
			"ws-dev": {},
			"ws-tgt": {{ID: "rep-id", DisplayName: "Rep", Type: "Report", WorkspaceID: "ws-tgt"}},
		},
		defByID: map[string]*fabric.Definition{},
	}
	rb := newDeployGroupsRebinder(t, fake)
	rb.SetSubstitutions([]deploy.Substitution{{
		ItemType: "Report", ItemName: "Rep",
		FindValue: "placeholder", TargetType: "Lakehouse", TargetName: "LH_Missing",
	}})
	// byPath pbir → no binding unresolved; the custom-sub ref is the only one.
	report := deploy.LocalItem{Type: "Report", DisplayName: "Rep", FolderPath: "Frontend",
		Parts: []deploy.Part{{Path: "definition.pbir",
			Content: []byte(`{"datasetReference":{"byPath":{"path":"../M.SemanticModel"}}}`)}}}

	customer := config.Customer{RepoPath: "/repo"}
	groups, err := buildDeployGroups(fake, "tok", customer,
		[]config.DeployMapping{{Folder: "Frontend", Workspace: "TGT"}},
		map[string][]deploy.LocalItem{customer.RepoPath: {report}}, fake.workspaces, &rebinderSet{shared: rb}, nil)
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	var customSub int
	for _, u := range groups[0].Unresolved {
		if u.Location == "custom substitution" {
			customSub++
			if u.ItemName != "Rep" {
				t.Errorf("custom-sub unresolved must carry ItemName=Rep, got %+v", u)
			}
		}
	}
	if customSub != 1 {
		t.Errorf("existing report's custom-substitution unresolved must surface exactly once, got %d (%+v)", customSub, groups[0].Unresolved)
	}
}

func TestHistoryDirAbsoluteUsedAsIs(t *testing.T) {
	// An absolute deploy-history path must NOT be joined onto the repo path
	// (joining two absolute paths produced the doubled repo/repo/file path).
	if got := historyDir("/repo/x", "/abs/elsewhere"); got != "/abs/elsewhere" {
		t.Errorf("absolute history path must be used as-is, got %q", got)
	}
}

func TestHistoryDirRelativeJoined(t *testing.T) {
	if got := historyDir("/repo/x", "docs/deploy"); got != "/repo/x/docs/deploy" {
		t.Errorf("relative history path must join onto repo, got %q", got)
	}
}

func TestTerminalLinkWrapsOSC8(t *testing.T) {
	got := terminalLink("file:///a/b.html", "/a/b.html")
	want := "\x1b]8;;file:///a/b.html\x1b\\/a/b.html\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("OSC 8 hyperlink mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestPrintUnresolvedListsRefs(t *testing.T) {
	groups := []deployGroup{{
		Folder: "Backend",
		Target: fabric.Workspace{DisplayName: "DW - TEST - Config"},
		Unresolved: []deploy.UnresolvedRef{
			{GUID: "0b0b0b0b-aaaa-bbbb-cccc-ddddeeeeffff", ItemType: "Lakehouse", Location: "known_lakehouses", ItemName: "NB_Config"},
		},
	}}
	out := captureStdout(t, func() { printUnresolved(groups, "DEV", "TEST") })
	if !strings.Contains(out, "NB_Config") || !strings.Contains(out, "0b0b0b0b") {
		t.Errorf("unresolved output missing context:\n%s", out)
	}
	if !strings.Contains(out, "Lakehouse") {
		t.Errorf("unresolved output missing type guess:\n%s", out)
	}
}

func TestPrintUnresolvedSilentWhenNone(t *testing.T) {
	out := captureStdout(t, func() { printUnresolved([]deployGroup{{Folder: "Backend"}}, "DEV", "TEST") })
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

func TestBuildDeployPickRows(t *testing.T) {
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "WS-A"},
		Rows: []deploy.CompareRow{
			{Class: deploy.ClassChanged, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Z"}},
			{Class: deploy.ClassUnchanged, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Skip"}},
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_A"}},
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_Gone"}, DeployedID: "gone-id"},
		},
	}}
	items, entries, title := buildDeployPickRows(groups)

	// Unchanged filtered out; New, Changed, and Orphan kept.
	if len(items) != 3 || len(entries) != 3 {
		t.Fatalf("want 3 rows (New+Changed+Orphan), got %d items / %d entries", len(items), len(entries))
	}
	// Sorted New before Changed before Orphan.
	if !strings.Contains(items[0].Label, "NB_A") {
		t.Errorf("New should sort first, got %q", items[0].Label)
	}
	if !strings.Contains(items[1].Label, "NB_Z") {
		t.Errorf("Changed should sort after New, got %q", items[1].Label)
	}
	if !strings.Contains(items[2].Label, "NB_Gone") {
		t.Errorf("Orphan should sort last, got %q", items[2].Label)
	}
	// Unchanged row absent; Orphan present.
	for _, it := range items {
		if strings.Contains(it.Label, "NB_Skip") {
			t.Errorf("unchanged leaked into picker: %q", it.Label)
		}
	}
	// Nothing pre-checked.
	for _, it := range items {
		if it.Checked {
			t.Errorf("rows must start unchecked: %q", it.Label)
		}
	}
	// Single target → workspace in the title, not in the row labels.
	if !strings.Contains(title, "WS-A") {
		t.Errorf("single-target title should name the workspace, got %q", title)
	}
	if strings.Contains(items[0].Label, "WS-A") {
		t.Errorf("single target must not put workspace in the row label: %q", items[0].Label)
	}
	// Index identity: deploy entries align with items.
	if entries[0].item.DisplayName != "NB_A" || entries[1].item.DisplayName != "NB_Z" {
		t.Errorf("deploy entries not index-aligned with items: %+v", entries)
	}
	// Orphan entry carries a DeleteTarget, not a deploy item.
	if entries[2].delete == nil || entries[2].delete.ID != "gone-id" {
		t.Errorf("orphan entry must carry DeleteTarget with correct ID, got %+v", entries[2])
	}
}

func TestBuildDeployPickRowsMultiTargetSuffixAndCollision(t *testing.T) {
	// Same type+name in two different target workspaces — must stay distinct
	// (the old label-keyed map collapsed them).
	groups := []deployGroup{
		{Target: fabric.Workspace{DisplayName: "WS-A"}, Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Dup"}}}},
		{Target: fabric.Workspace{DisplayName: "WS-B"}, Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Dup"}}}},
	}
	items, entries, _ := buildDeployPickRows(groups)
	if len(items) != 2 || len(entries) != 2 {
		t.Fatalf("both same-named items must survive, got %d", len(items))
	}
	// Multiple targets → each row carries its workspace. Both rows tie on every
	// sort key (New/Notebook/NB_Dup), so the WS-A-before-WS-B order holds only
	// because sort.SliceStable preserves the group-iteration order.
	if !strings.Contains(items[0].Label, "WS-A") || !strings.Contains(items[1].Label, "WS-B") {
		t.Errorf("multi-target rows must show their workspace: %q / %q", items[0].Label, items[1].Label)
	}
	// Distinct entries point at the two different groups.
	if entries[0].gi == entries[1].gi {
		t.Errorf("collided to one group: %+v", entries)
	}
}

func TestBuildDeployPickRowsEmpty(t *testing.T) {
	items, entries, title := buildDeployPickRows(nil)
	if len(items) != 0 || len(entries) != 0 {
		t.Errorf("empty input → no rows, got %d items / %d entries", len(items), len(entries))
	}
	if title != "Select items to deploy" {
		t.Errorf("empty title = %q, want %q", title, "Select items to deploy")
	}
}

func TestRunDeployDeletesOnlyOnDeleteConfirm(t *testing.T) {
	groups := []deployGroup{makeGroup("F", "ws1", "WS",
		[]deploy.LocalItem{{Type: "Notebook", DisplayName: "NB", LogicalID: "lid"}}, nil)}
	selectWithDelete := func(gs []deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
		return map[int][]deploy.LocalItem{0: {gs[0].Rows[0].Local}},
			map[int][]deploy.DeleteTarget{0: {{ID: "x", Name: "NB_Gone", Type: "Notebook"}}}, nil
	}
	hasDelete := func(res []deploy.Result) bool {
		for _, r := range res {
			if r.Action == deploy.ActionDelete {
				return true
			}
		}
		return false
	}
	newFake := func() *deployFakeAPI {
		return &deployFakeAPI{workspaces: []fabric.Workspace{{ID: "ws1", DisplayName: "WS"}}}
	}

	// Deploy confirm yes, delete confirm NO → no delete runs.
	calls := 0
	res, err := runDeploy(newFake(), "tok", groups, selectWithDelete,
		func(string) (bool, error) { calls++; return calls == 1, nil }, false) // 1st (deploy) yes, 2nd (delete) no
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if hasDelete(res) {
		t.Errorf("delete ran despite a declined delete confirm")
	}

	// Both confirms yes → the delete runs.
	res2, err := runDeploy(newFake(), "tok", groups, selectWithDelete,
		func(string) (bool, error) { return true, nil }, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if !hasDelete(res2) {
		t.Errorf("delete should run when the delete confirm is yes")
	}
}

func TestRunDeployDeleteOnly(t *testing.T) {
	// Only an orphan to delete, no deploys: the deploy confirm must be SKIPPED
	// (no "Deploy 0 item(s)?") and the delete must run on its own affirmative
	// confirm — the primary "just clean up orphans" use case.
	fake := &deployFakeAPI{workspaces: []fabric.Workspace{{ID: "ws1", DisplayName: "WS"}}}
	groups := []deployGroup{makeGroup("F", "ws1", "WS", nil, nil)}
	selectDeleteOnly := func(gs []deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
		return map[int][]deploy.LocalItem{},
			map[int][]deploy.DeleteTarget{0: {{ID: "x", Name: "NB_Gone", Type: "Notebook"}}}, nil
	}
	calls := 0
	res, err := runDeploy(fake, "tok", groups, selectDeleteOnly,
		func(string) (bool, error) { calls++; return true, nil }, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if calls != 1 {
		t.Errorf("delete-only run should prompt exactly once (the delete confirm), got %d", calls)
	}
	var deleted bool
	for _, r := range res {
		if r.Action == deploy.ActionDelete {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("delete-only run with an affirmative confirm must run the delete")
	}
}

func TestPrintDeployResultsCountsDeletesSeparately(t *testing.T) {
	results := []deploy.Result{
		{Name: "NB_A", Type: "Notebook", Action: deploy.ActionCreate},
		{Name: "NB_Gone", Type: "Notebook", Action: deploy.ActionDelete},
		{Name: "NB_Gone2", Type: "Notebook", Action: deploy.ActionDelete},
	}
	out := captureStdout(t, func() { printDeployResults(results) })
	if !strings.Contains(out, "Deployed 1 item(s)") || !strings.Contains(out, "deleted 2") {
		t.Errorf("headline must separate deploys from deletes (Deployed 1, deleted 2), got:\n%s", out)
	}
	if strings.Contains(out, "Deployed 3") {
		t.Errorf("deleted items must not be counted as deployed:\n%s", out)
	}
}

func TestPrintDeployResultsBothErrAndWarning(t *testing.T) {
	// A result with BOTH Err and Warning set must surface both in the output.
	// Before the fix, the switch falls into the Err case and the warning is
	// silently dropped.
	results := []deploy.Result{
		{
			Name:    "NB_Broken",
			Type:    "Notebook",
			Action:  deploy.ActionUpdate,
			Err:     fmt.Errorf("upload failed: timeout"),
			Warning: "description not synced",
		},
	}
	out := captureStdout(t, func() { printDeployResults(results) })
	if !strings.Contains(out, "upload failed: timeout") {
		t.Errorf("error text missing from output:\n%s", out)
	}
	if !strings.Contains(out, "description not synced") {
		t.Errorf("warning text missing from output — was silently dropped:\n%s", out)
	}
}

func TestRunDeployDeleteConfirmNamesWorkspace(t *testing.T) {
	fake := &deployFakeAPI{workspaces: []fabric.Workspace{{ID: "ws1", DisplayName: "WS-Prod"}}}
	groups := []deployGroup{makeGroup("F", "ws1", "WS-Prod", nil, nil)}
	selectDel := func(gs []deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
		return map[int][]deploy.LocalItem{},
			map[int][]deploy.DeleteTarget{0: {{ID: "x", Name: "NB_Gone", Type: "Notebook"}}}, nil
	}
	var deletePrompt string
	_, err := runDeploy(fake, "tok", groups, selectDel, func(p string) (bool, error) {
		if strings.Contains(p, "DELETE") {
			deletePrompt = p
		}
		return false, nil // decline — we only assert the prompt text
	}, false)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if !strings.Contains(deletePrompt, "WS-Prod") {
		t.Errorf("delete confirm must name the target workspace, got %q", deletePrompt)
	}
}

func TestReconcileOrphansSharedWorkspace(t *testing.T) {
	// Two folders → the SAME workspace. Each folder's Compare ran against the
	// workspace's full deployed list, so each group flags the sibling's valid
	// item as an Orphan, and the one true orphan (NB_Gone) appears in both.
	ws := fabric.Workspace{ID: "ws1", DisplayName: "WS"}
	groups := []deployGroup{
		{Folder: "A", Target: ws, Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_A"}},
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_B"}, DeployedID: "b"},    // sibling's real item
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_Gone"}, DeployedID: "g"}, // true orphan
		}},
		{Folder: "B", Target: ws, Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_B"}},
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_A"}, DeployedID: "a"},    // sibling's real item
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_Gone"}, DeployedID: "g"}, // true orphan (dup)
		}},
	}
	reconcileOrphans(groups)

	orphans, gone := 0, 0
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.Class == deploy.ClassOrphan {
				orphans++
				switch r.Name() {
				case "NB_Gone":
					gone++
				case "NB_A", "NB_B":
					t.Errorf("sibling folder's valid item %q wrongly kept as a deletable orphan", r.Name())
				}
			}
		}
	}
	if orphans != 1 || gone != 1 {
		t.Errorf("want exactly 1 orphan (NB_Gone once), got %d orphan(s), %d NB_Gone", orphans, gone)
	}
	// The New rows must survive untouched.
	if len(groups[0].Rows) != 2 || len(groups[1].Rows) != 1 {
		t.Errorf("non-orphan rows altered: groupA=%d groupB=%d rows", len(groups[0].Rows), len(groups[1].Rows))
	}
}

func TestReconcileOrphansSingleMappingNoOp(t *testing.T) {
	// One folder → one workspace: a true orphan stays, nothing is dropped.
	ws := fabric.Workspace{ID: "ws1", DisplayName: "WS"}
	groups := []deployGroup{{Folder: "A", Target: ws, Rows: []deploy.CompareRow{
		{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_A"}},
		{Class: deploy.ClassOrphan, Deployed: fabric.Item{Type: "Notebook", DisplayName: "NB_Gone"}, DeployedID: "g"},
	}}}
	reconcileOrphans(groups)
	if len(groups[0].Rows) != 2 {
		t.Fatalf("single-mapping reconcile should be a no-op, got %d rows", len(groups[0].Rows))
	}
}

func TestBuildDeployPickRowsIncludesOrphans(t *testing.T) {
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "WS-A"},
		Rows: []deploy.CompareRow{
			{Class: deploy.ClassNew, Local: deploy.LocalItem{Type: "Notebook", DisplayName: "NB_New"}},
			{Class: deploy.ClassOrphan, Deployed: fabric.Item{ID: "orphan-id", Type: "Notebook", DisplayName: "NB_Gone"}, DeployedID: "orphan-id"},
		},
	}}
	items, entries, _ := buildDeployPickRows(groups)
	if len(items) != 2 {
		t.Fatalf("want New + Orphan rows, got %d", len(items))
	}
	// Orphan sorts last, is SkipBulkSelect, and carries a DeleteTarget entry.
	if !strings.Contains(items[1].Label, "NB_Gone") {
		t.Fatalf("orphan should sort last, got %q", items[1].Label)
	}
	if !items[1].SkipBulkSelect {
		t.Errorf("orphan row must be SkipBulkSelect")
	}
	if entries[1].delete == nil || entries[1].delete.ID != "orphan-id" || entries[1].delete.Name != "NB_Gone" {
		t.Errorf("orphan entry must carry a DeleteTarget, got %+v", entries[1])
	}
	// The deploy row carries no delete target.
	if entries[0].delete != nil {
		t.Errorf("deploy row must not be a delete: %+v", entries[0])
	}
}

func TestRunDeployBulkBackendCallsBulkImport(t *testing.T) {
	api := &deployFakeAPI{
		bulkResult: &fabric.BulkImportResult{Details: []fabric.BulkImportDetail{
			{ItemID: "sales-id", ItemDisplayName: "Sales", ItemType: "SemanticModel", OperationType: "Create", OperationStatus: "Succeeded"},
		}},
	}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Prod"}
	groups := []deployGroup{{Target: target, Folder: "models", Rows: nil, Deployed: nil}}
	item := deploy.LocalItem{
		Type: "SemanticModel", DisplayName: "Sales", FolderPath: "models/Sales.SemanticModel",
		Platform: []byte(`{"metadata":{"type":"SemanticModel","displayName":"Sales"},"config":{"logicalId":"x"}}`),
		Parts:    []deploy.Part{{Path: "definition/model.tmdl", Content: []byte("model Sales")}},
	}
	selectItems := func([]deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
		return map[int][]deploy.LocalItem{0: {item}}, nil, nil
	}
	confirm := func(string) (bool, error) { return true, nil }

	results, err := runDeploy(api, "tok", groups, selectItems, confirm, true)
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if len(api.bulkWS) != 1 || api.bulkWS[0] != "ws-1" {
		t.Fatalf("expected one bulk call to ws-1, got %v", api.bulkWS)
	}
	if len(results) != 1 || results[0].ID != "sales-id" || results[0].Action != deploy.ActionCreate {
		t.Errorf("results = %+v", results)
	}
	// The per-item CreateItem path must NOT have run.
	if len(api.created) != 0 {
		t.Errorf("per-item CreateItem should not run in bulk mode, got %v", api.created)
	}
}

func TestPrintReportBindingsRendersResolved(t *testing.T) {
	groups := []deployGroup{{
		ReportBindings: []deploy.ReportBinding{
			{Report: "Daniel - Testing", Model: "HR", Workspace: "DW - TEST - SemMod"},
		},
	}}
	out := captureStdout(t, func() { printReportBindings(groups) })
	if !strings.Contains(out, "Daniel - Testing") ||
		!strings.Contains(out, "HR") ||
		!strings.Contains(out, "DW - TEST - SemMod") {
		t.Errorf("report binding line missing detail:\n%s", out)
	}
}

func TestPrintReportBindingsSilentWhenEmpty(t *testing.T) {
	out := captureStdout(t, func() { printReportBindings(nil) })
	if out != "" {
		t.Errorf("expected no output for no bindings, got %q", out)
	}
}

func TestRepoInputsForAlias(t *testing.T) {
	c := config.Customer{
		RepoPath: "/repos/primary",
		Environments: []config.Environment{{
			Alias: "DEV",
			Deployments: []config.DeployMapping{
				{Folder: "FabricBackEnd", Workspace: "WS-A"},                  // primary
				{Folder: "", Workspace: "WS-B", Repo: "/repos/frontend"},      // frontend
				{Folder: "Extra", Workspace: "WS-C", Repo: "/repos/frontend"}, // dup frontend
			},
		}},
	}
	got := repoInputsForAlias(c, "DEV")
	want := []string{"/repos/primary", "/repos/frontend"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repoInputsForAlias = %v, want %v", got, want)
	}
}

func TestRepoInputsForAliasNoRepoPath(t *testing.T) {
	// A customer with only explicit per-mapping repos (no primary set yet).
	c := config.Customer{
		Environments: []config.Environment{{
			Alias:       "DEV",
			Deployments: []config.DeployMapping{{Repo: "/repos/only", Workspace: "W"}},
		}},
	}
	if got := repoInputsForAlias(c, "DEV"); !reflect.DeepEqual(got, []string{"/repos/only"}) {
		t.Fatalf("got %v, want [/repos/only]", got)
	}
}

// TestBuildDeployGroupsPerRepo proves each mapping pulls items from its own
// repo (via customer.MappingRepo), not from a single shared slice — the
// primary mapping (no Repo set) must only see items from the customer's
// RepoPath, and a mapping with an explicit Repo must only see that repo's
// items.
func TestBuildDeployGroupsPerRepo(t *testing.T) {
	nbA := deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Back", FolderPath: "", LogicalID: "lid-a"}
	nbB := deploy.LocalItem{Type: "Notebook", DisplayName: "NB_Front", FolderPath: "", LogicalID: "lid-b"}
	itemsByRepo := map[string][]deploy.LocalItem{
		"/repos/backend":  {nbA},
		"/repos/frontend": {nbB},
	}
	customer := config.Customer{
		RepoPath: "/repos/backend",
		Environments: []config.Environment{{Alias: "DEV",
			Deployments: []config.DeployMapping{
				{Folder: "", Workspace: "WS-Back"},                           // primary → backend
				{Folder: "", Workspace: "WS-Front", Repo: "/repos/frontend"}, // frontend
			}}},
	}
	mappings := customer.Environments[0].Deployments
	ws := []fabric.Workspace{{ID: "WS-Back", DisplayName: "WS-Back"}, {ID: "WS-Front", DisplayName: "WS-Front"}}
	rf := &deployFakeAPI{ /* itemsByWS empty: everything is New */ }

	groups, err := buildDeployGroups(rf, "tok", customer, mappings, itemsByRepo, ws, nil, nil)
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	// Group for WS-Back must contain NB_Back only; WS-Front must contain NB_Front only.
	byWS := map[string][]string{}
	for _, g := range groups {
		for _, r := range g.Rows {
			byWS[g.Target.DisplayName] = append(byWS[g.Target.DisplayName], r.Name())
		}
	}
	if !reflect.DeepEqual(byWS["WS-Back"], []string{"NB_Back"}) {
		t.Fatalf("WS-Back rows = %v, want [NB_Back]", byWS["WS-Back"])
	}
	if !reflect.DeepEqual(byWS["WS-Front"], []string{"NB_Front"}) {
		t.Fatalf("WS-Front rows = %v, want [NB_Front]", byWS["WS-Front"])
	}
}

func (f *deployFakeAPI) SetVariableLibraryActiveSet(token, ws, id, valueSetName string) error {
	return nil
}

func (f *deployFakeAPI) PublishEnvironment(token, ws, id string) error {
	if f.envPublishErr != nil {
		return f.envPublishErr
	}
	f.envPublished = append(f.envPublished, id)
	return nil
}

// GetEnvironmentPublishState pops the next state from the item's primed
// sequence, holding the final entry once the sequence is exhausted.
func (f *deployFakeAPI) GetEnvironmentPublishState(token, ws, id string) (string, error) {
	f.envStatePolls++
	seq := f.envStates[id]
	if len(seq) == 0 {
		return "success", nil
	}
	state := seq[0]
	if len(seq) > 1 {
		f.envStates[id] = seq[1:]
	}
	return state, nil
}

// TestActivateVariableLibraries: a deployed VariableLibrary whose settings.json
// names a value set after the target env gets it activated; no match leaves the
// target untouched; deletes and failures are never activated.
func TestActivateVariableLibraries(t *testing.T) {
	settings := func(sets ...string) []byte {
		b, _ := json.Marshal(map[string]any{"valueSetsOrder": sets})
		return b
	}
	local := func(name string, settingsJSON []byte) deploy.LocalItem {
		return deploy.LocalItem{
			Type: "VariableLibrary", DisplayName: name,
			Parts: []deploy.Part{{Path: "settings.json", Content: settingsJSON}},
		}
	}
	groups := []deployGroup{{Rows: []deploy.CompareRow{
		{Class: deploy.ClassChanged, Local: local("VL_Match", settings("TEST", "PROD"))},
		{Class: deploy.ClassChanged, Local: local("VL_NoMatch", settings("PROD"))},
		{Class: deploy.ClassChanged, Local: local("VL_Deleted", settings("TEST"))},
	}}}
	results := []deploy.Result{
		{Name: "VL_Match", Type: "VariableLibrary", Action: deploy.ActionUpdate, ID: "id-1", WorkspaceID: "ws-1"},
		{Name: "VL_NoMatch", Type: "VariableLibrary", Action: deploy.ActionUpdate, ID: "id-2", WorkspaceID: "ws-1"},
		{Name: "VL_Deleted", Type: "VariableLibrary", Action: deploy.ActionDelete, ID: "id-3", WorkspaceID: "ws-1"},
		{Name: "nb_x", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-4", WorkspaceID: "ws-1"},
	}
	fake := &activationFakeAPI{}
	activateVariableLibraries(fake, "tok", "TEST", groups, results)
	if len(fake.activated) != 1 {
		t.Fatalf("activations = %v, want exactly VL_Match", fake.activated)
	}
	if got := fake.activated[0]; got != [3]string{"ws-1", "id-1", "TEST"} {
		t.Errorf("activated %v", got)
	}
}

// TestPublishEnvironments: every successfully deployed Environment is
// submitted for staging→publish and polled to a settled state; deletes,
// failures and other types are never submitted; declining the confirm
// submits nothing.
func TestPublishEnvironments(t *testing.T) {
	restoreInterval, restorePolls := envPublishPollInterval, envPublishMaxPolls
	envPublishPollInterval, envPublishMaxPolls = 0, 10
	t.Cleanup(func() { envPublishPollInterval, envPublishMaxPolls = restoreInterval, restorePolls })

	results := []deploy.Result{
		{Name: "ENV_A", Type: "Environment", Action: deploy.ActionUpdate, ID: "env-a", WorkspaceID: "ws-1"},
		{Name: "ENV_B", Type: "Environment", Action: deploy.ActionCreate, ID: "env-b", WorkspaceID: "ws-1"},
		{Name: "ENV_Err", Type: "Environment", Action: deploy.ActionUpdate, Err: errors.New("boom"), WorkspaceID: "ws-1"},
		{Name: "ENV_Del", Type: "Environment", Action: deploy.ActionDelete, ID: "env-del", WorkspaceID: "ws-1"},
		{Name: "nb_x", Type: "Notebook", Action: deploy.ActionUpdate, ID: "nb-1", WorkspaceID: "ws-1"},
	}
	fake := &deployFakeAPI{envStates: map[string][]string{
		"env-a": {"running", "running", "success"},
		"env-b": {"running", "failed"},
	}}
	publishEnvironments(fake, "tok", results, func(string) (bool, error) { return true, nil })

	if !reflect.DeepEqual(fake.envPublished, []string{"env-a", "env-b"}) {
		t.Errorf("published = %v, want [env-a env-b]", fake.envPublished)
	}
	// Both must be polled to their settled state (success resp. failed) —
	// 3 + 2 sequenced polls, no polls after settling.
	if fake.envStatePolls < 5 {
		t.Errorf("expected ≥5 state polls, got %d", fake.envStatePolls)
	}

	// Declining the confirm leaves everything staged.
	fake = &deployFakeAPI{}
	publishEnvironments(fake, "tok", results, func(string) (bool, error) { return false, nil })
	if len(fake.envPublished) != 0 {
		t.Errorf("declined confirm must not publish, got %v", fake.envPublished)
	}

	// No environments in the results → the confirm must not even be asked.
	publishEnvironments(fake, "tok", results[4:], func(string) (bool, error) {
		t.Fatal("confirm called with no environments deployed")
		return false, nil
	})
}

// activationFakeAPI records SetVariableLibraryActiveSet calls; everything else
// is unused by activateVariableLibraries.
type activationFakeAPI struct {
	deployFakeAPI
	activated [][3]string
}

func (f *activationFakeAPI) SetVariableLibraryActiveSet(token, ws, id, valueSetName string) error {
	f.activated = append(f.activated, [3]string{ws, id, valueSetName})
	return nil
}
