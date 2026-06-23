package cmd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
func (f *deployFakeAPI) DeleteItem(token, ws, id string) error                           { return nil }
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
	rows := deploy.Compare(local, deployed, localTypeScope(local))
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
	rows := deploy.Compare(local, deployed, localTypeScope(local))
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

	// Nothing published (empty results) → no report, no folder created.
	_ = captureStdout(t, func() { saveDeployHistory(customer, groups, nil) })
	if entries, _ := os.ReadDir(filepath.Join(repo, "history")); len(entries) != 0 {
		t.Errorf("empty results must write no report, found %d file(s)", len(entries))
	}

	// Items deployed → a .html report appears in the configured folder.
	_ = captureStdout(t, func() { saveDeployHistory(customer, groups, results) })
	entries, err := os.ReadDir(filepath.Join(repo, "history"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 report file, got %d (err %v)", len(entries), err)
	}
	if !strings.HasSuffix(entries[0].Name(), ".html") {
		t.Errorf("report should be .html, got %q", entries[0].Name())
	}

	// Deployed but history unconfigured → skip notice, no panic.
	out := captureStdout(t, func() {
		saveDeployHistory(config.Customer{RepoPath: repo}, groups, results)
	})
	if !strings.Contains(out, "No deploy-history folder set") {
		t.Errorf("expected skip notice when unconfigured, got %q", out)
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
	res, err := runDeploy(fake, "tok", "", groups, nil, selectAll, func(string) (bool, error) { return true, nil })
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

	res, err := runDeploy(fake, "tok", "", groups, nil, selectAll, func(string) (bool, error) { return false, nil })
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
	res, err := runDeploy(fake, "tok", "", groups, nil, selectAll, func(string) (bool, error) { return true, nil })
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
	res, err := runDeploy(newFake(), "tok", "", groups, nil, selectWithDelete,
		func(string) (bool, error) { calls++; return calls == 1, nil }) // 1st (deploy) yes, 2nd (delete) no
	if err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if hasDelete(res) {
		t.Errorf("delete ran despite a declined delete confirm")
	}

	// Both confirms yes → the delete runs.
	res2, err := runDeploy(newFake(), "tok", "", groups, nil, selectWithDelete,
		func(string) (bool, error) { return true, nil })
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
	res, err := runDeploy(fake, "tok", "", groups, nil, selectDeleteOnly,
		func(string) (bool, error) { calls++; return true, nil })
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
	_, err := runDeploy(fake, "tok", "TEST", groups, nil, selectDel, func(p string) (bool, error) {
		if strings.Contains(p, "DELETE") {
			deletePrompt = p
		}
		return false, nil // decline — we only assert the prompt text
	})
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
