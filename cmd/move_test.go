package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// fakeMoveAPI is the test double for the Move flow. Counters let
// us assert which methods were called and how often without
// reaching into the full responses.
type fakeMoveAPI struct {
	token       string
	workspaces  []fabric.Workspace
	items       map[string][]fabric.Item      // workspaceID -> items
	definitions map[string]*fabric.Definition // itemID -> definition

	rebindCalls       int
	createCalls       int
	updateCalls       int
	lastCreateName    string
	createNames       []string        // every CreateItem name, in call order
	failCreateNames   map[string]bool // CreateItem fails for these names only
	lastUpdateItemID  string
	lastRebindDataset string
	rebindByReport    map[string]string // report ID -> dataset it was rebound to
	rebindErr         error
	createErr         error
}

func (f *fakeMoveAPI) GetAccessToken(string) (string, error) { return f.token, nil }
func (f *fakeMoveAPI) GetWorkspaceID(_, name string) (string, error) {
	for _, w := range f.workspaces {
		if w.DisplayName == name {
			return w.ID, nil
		}
	}
	return "", errors.New("workspace not found")
}
func (f *fakeMoveAPI) ListWorkspaces(string) ([]fabric.Workspace, error) {
	return f.workspaces, nil
}
func (f *fakeMoveAPI) ListNotebooks(_, ws string) ([]fabric.Item, error) {
	var out []fabric.Item
	for _, it := range f.items[ws] {
		if it.Type == "Notebook" {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *fakeMoveAPI) GetNotebookIpynb(string, string, string) ([]byte, error) {
	return nil, errors.New("not used by move tests")
}
func (f *fakeMoveAPI) RunPipeline(string, string, string, map[string]any) (string, error) {
	return "", fmt.Errorf("not used by move tests")
}
func (f *fakeMoveAPI) RunNotebook(string, string, string, []fabric.JobInput, *fabric.DefaultLakehouse) (string, error) {
	return "", errors.New("not used by move tests")
}
func (f *fakeMoveAPI) GetJobInstance(string, string) (fabric.JobInstanceStatus, error) {
	return fabric.JobInstanceStatus{}, errors.New("not used by move tests")
}
func (f *fakeMoveAPI) ListItems(_, ws string) ([]fabric.Item, error) {
	return f.items[ws], nil
}
func (f *fakeMoveAPI) ListItemsByType(_, ws, typ string) ([]fabric.Item, error) {
	var out []fabric.Item
	for _, it := range f.items[ws] {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *fakeMoveAPI) GetItemDefinition(_, _, itemID, _ string) (*fabric.Definition, error) {
	if d, ok := f.definitions[itemID]; ok {
		return d, nil
	}
	return &fabric.Definition{Parts: []fabric.DefinitionPart{{Path: "x", Payload: "Zm9v", PayloadType: "InlineBase64"}}}, nil
}
func (f *fakeMoveAPI) CreateItem(_, ws, name, typ string, _ *fabric.Definition, _ json.RawMessage, _ string) (fabric.Item, error) {
	f.createCalls++
	f.lastCreateName = name
	f.createNames = append(f.createNames, name)
	if f.createErr != nil {
		return fabric.Item{}, f.createErr
	}
	if f.failCreateNames[name] {
		return fabric.Item{}, fmt.Errorf("create %s: 500 boom", name)
	}
	created := fabric.Item{ID: "new-" + name, DisplayName: name, Type: typ, WorkspaceID: ws}
	f.items[ws] = append(f.items[ws], created)
	return created, nil
}
func (f *fakeMoveAPI) UpdateItemDefinition(_, _, itemID string, _ *fabric.Definition) error {
	f.updateCalls++
	f.lastUpdateItemID = itemID
	return nil
}
func (f *fakeMoveAPI) UpdateItem(_, _, _, _, _ string) error { return nil }
func (f *fakeMoveAPI) DeleteItem(_, _, _ string) error       { return nil }
func (f *fakeMoveAPI) RebindReport(_, _, reportID, datasetID string) error {
	f.rebindCalls++
	f.lastRebindDataset = datasetID
	if f.rebindByReport == nil {
		f.rebindByReport = map[string]string{}
	}
	f.rebindByReport[reportID] = datasetID
	return f.rebindErr
}

// Refresh flow methods — the Move tests never call these. Return loud
// errors so an accidental reuse in a Refresh test surfaces instead of
// silently passing with empty data.
func (f *fakeMoveAPI) ListDatasets(string, string) ([]fabric.Dataset, error) {
	return nil, errors.New("ListDatasets not used by move tests")
}
func (f *fakeMoveAPI) QueryRefreshableTables(string, string, string) ([]string, error) {
	return nil, errors.New("QueryRefreshableTables not used by move tests")
}
func (f *fakeMoveAPI) TriggerRefresh(string, string, string, []string) (string, error) {
	return "", errors.New("TriggerRefresh not used by move tests")
}
func (f *fakeMoveAPI) WaitForRefresh(string, string, string, string) (fabric.RefreshStatus, error) {
	return fabric.RefreshStatus{}, errors.New("WaitForRefresh not used by move tests")
}

// Deploy flow methods — not used by move tests.
func (f *fakeMoveAPI) GetLakehouseSqlEndpoint(string, string, string) (string, string, error) {
	return "", "", errors.New("GetLakehouseSqlEndpoint not used by move tests")
}
func (f *fakeMoveAPI) BulkImportDefinitions(token, ws string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error) {
	return &fabric.BulkImportResult{}, nil
}

// Workspace management methods — not used by move tests.
func (f *fakeMoveAPI) GetWorkspace(string, string) (fabric.Workspace, error) {
	return fabric.Workspace{}, errors.New("GetWorkspace not used by move tests")
}
func (f *fakeMoveAPI) ListWorkspacesByRole(string, string) ([]fabric.Workspace, error) {
	return nil, errors.New("ListWorkspacesByRole not used by move tests")
}
func (f *fakeMoveAPI) CreateWorkspace(string, string, string, string) (fabric.Workspace, error) {
	return fabric.Workspace{}, errors.New("CreateWorkspace not used by move tests")
}
func (f *fakeMoveAPI) RenameWorkspace(string, string, string) (fabric.Workspace, error) {
	return fabric.Workspace{}, errors.New("RenameWorkspace not used by move tests")
}
func (f *fakeMoveAPI) SetWorkspaceDescription(string, string, string) (fabric.Workspace, error) {
	return fabric.Workspace{}, errors.New("SetWorkspaceDescription not used by move tests")
}
func (f *fakeMoveAPI) DeleteWorkspace(string, string) error {
	return errors.New("DeleteWorkspace not used by move tests")
}
func (f *fakeMoveAPI) ListCapacities(string) ([]fabric.Capacity, error) {
	return nil, errors.New("ListCapacities not used by move tests")
}

// withMovePickers installs deterministic pickers for the duration
// of a single test. The filter picker picks the option whose Label
// matches filterPick (or first option if filterPick is empty); the
// number picker is driven by a queue of pre-set values that match
// each menu in order. promptInput just returns promptReturn.
func withMovePickers(t *testing.T, filterPicks []string, numberPicks []string, promptReturn string) func() {
	t.Helper()
	origFilter := moveFilterPicker
	origMulti := moveMultiPicker
	origNumber := moveNumberPicker
	origPrompt := movePromptInput
	origConfirm := moveConfirm

	filterIdx := 0
	numberIdx := 0
	moveMultiPicker = func(_ string, items []ui.CheckItem) ([]int, error) {
		if filterIdx >= len(filterPicks) {
			return nil, errors.New("ran out of filter picks (item picker)")
		}
		target := filterPicks[filterIdx]
		filterIdx++
		return multiPickByNames(target)("", items)
	}
	moveFilterPicker = func(_ string, options []ui.FilterOption, _ ui.FilterRowRenderer) (string, error) {
		if filterIdx >= len(filterPicks) {
			return "", errors.New("ran out of filter picks")
		}
		target := filterPicks[filterIdx]
		filterIdx++
		if target == "" {
			return options[0].Value, nil
		}
		for _, o := range options {
			if o.Label == target {
				return o.Value, nil
			}
		}
		return "", fmt.Errorf("no filter option labeled %q", target)
	}
	moveNumberPicker = func(_ string, options []ui.MenuOption) (string, error) {
		if numberIdx >= len(numberPicks) {
			return "", errors.New("ran out of number picks")
		}
		target := numberPicks[numberIdx]
		numberIdx++
		for _, o := range options {
			if o.Label == target || o.Value == target {
				return o.Value, nil
			}
		}
		return "", fmt.Errorf("no number option labeled %q", target)
	}
	movePromptInput = func(string, string) (string, error) { return promptReturn, nil }
	moveConfirm = func(string) (bool, error) { return true, nil }

	return func() {
		moveFilterPicker = origFilter
		moveMultiPicker = origMulti
		moveNumberPicker = origNumber
		movePromptInput = origPrompt
		moveConfirm = origConfirm
	}
}

// multiPickByNames returns an item-picker stub that checks the rows whose label
// starts with one of the given names (labels are "<name><padding>  <type>").
// No names means the user pressed enter with nothing checked.
func multiPickByNames(names ...string) func(string, []ui.CheckItem) ([]int, error) {
	return func(_ string, items []ui.CheckItem) ([]int, error) {
		var out []int
		for _, name := range names {
			if name == "" {
				continue
			}
			found := false
			for i, it := range items {
				if strings.HasPrefix(it.Label, name+" ") || it.Label == name {
					out = append(out, i)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("no item row labeled %q", name)
			}
		}
		return out, nil
	}
}

// multiPickQueue answers successive multi-picker calls with successive name
// sets: the item picker first, then any rebind-plan change pickers.
func multiPickQueue(picks ...[]string) func(string, []ui.CheckItem) ([]int, error) {
	i := 0
	return func(title string, items []ui.CheckItem) ([]int, error) {
		if i >= len(picks) {
			return nil, fmt.Errorf("unexpected multi picker call %q", title)
		}
		names := picks[i]
		i++
		return multiPickByNames(names...)(title, items)
	}
}

// writeTestConfig writes a single-customer config to a temp file
// and returns its path. The test cleans it up via t.TempDir.
func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/config.json"
	cfg := config.Config{Customers: map[string]config.Customer{
		"Acme": {Environments: []config.Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV"}}}},
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// --- table-driven cases ---

func TestMove_NotebookToOtherWorkspace_NoRebindOffered(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "nb-1", DisplayName: "LoadHR", Type: "Notebook", WorkspaceID: "ws-a"}},
			"ws-b": {},
		},
		definitions: map[string]*fabric.Definition{},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "LoadHR", "DW - feature"}, // source ws, item, dest ws
		nil, // no NumberMenu picks (no collision, no rebind)
		"")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI returned error: %v", err)
	}
	if api.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", api.createCalls)
	}
	if api.rebindCalls != 0 {
		t.Errorf("rebind must not be called for a Notebook, got %d calls", api.rebindCalls)
	}
}

func TestMove_ReportWithRebind_RebindCalledWithChosenDataset(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b": {{ID: "sm-1", DisplayName: "HR-Test", Type: "SemanticModel", WorkspaceID: "ws-b"}},
		},
		definitions: map[string]*fabric.Definition{},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "HR-Test"}, // src-ws, item, dest-ws, rebind-model
		nil,
		"")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.rebindCalls != 1 {
		t.Errorf("expected 1 rebind call, got %d", api.rebindCalls)
	}
	if api.lastRebindDataset != "sm-1" {
		t.Errorf("expected rebind to sm-1, got %q", api.lastRebindDataset)
	}
}

func TestMove_ReportSkipRebind_RebindNotCalled(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b": {{ID: "sm-1", DisplayName: "HR-Test", Type: "SemanticModel", WorkspaceID: "ws-b"}},
		},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "⋯ Skip (keep current binding)"},
		nil,
		"")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.rebindCalls != 0 {
		t.Errorf("Skip must not call rebind, got %d", api.rebindCalls)
	}
}

func TestMove_CollisionOverwrite_UsesUpdateNotCreate(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b": {
				{ID: "r-existing", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-b"},
				{ID: "sm-1", DisplayName: "HR-Test", Type: "SemanticModel", WorkspaceID: "ws-b"},
			},
		},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "⋯ Skip (keep current binding)"}, // 4 filter picks
		[]string{"Overwrite the existing item"},                                     // 1 number pick (collision menu)
		"")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.updateCalls != 1 || api.lastUpdateItemID != "r-existing" {
		t.Errorf("expected update on r-existing, got updateCalls=%d lastID=%q", api.updateCalls, api.lastUpdateItemID)
	}
	if api.createCalls != 0 {
		t.Errorf("overwrite must not create, got %d creates", api.createCalls)
	}
}

func TestMove_CollisionRename_CreatesWithNewName(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b": {{ID: "r-existing", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-b"}},
		},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "⋯ Skip (keep current binding)"}, // src-ws, item, dest-ws, rebind-skip
		[]string{"Create with a new name"},                                          // collision menu
		"HR-copy")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.createCalls != 1 || api.lastCreateName != "HR-copy" {
		t.Errorf("expected create with HR-copy, got createCalls=%d lastName=%q", api.createCalls, api.lastCreateName)
	}
}

func TestMove_CopyOkRebindFails_PartialSuccess(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b": {{ID: "sm-1", DisplayName: "HR-Test", Type: "SemanticModel", WorkspaceID: "ws-b"}},
		},
		rebindErr: errors.New("400 incompatible schema"),
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "HR-Test"}, // src-ws, item, dest-ws, rebind-model
		nil,
		"")
	defer restore()

	err := MoveWithAPI(writeTestConfig(t), api)
	if err != nil {
		t.Fatalf("partial success must return nil, got %v", err)
	}
	if api.createCalls != 1 {
		t.Errorf("expected 1 create call, got %d", api.createCalls)
	}
	if api.rebindCalls != 1 {
		t.Errorf("expected 1 rebind call (that failed), got %d", api.rebindCalls)
	}
}

func TestMove_NoSupportedItemsInSource_FriendlyEmpty(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "lh-1", DisplayName: "MyLake", Type: "Lakehouse", WorkspaceID: "ws-a"}},
			"ws-b": {},
		},
	}
	restore := withMovePickers(t, []string{"DW - DEV"}, nil, "")
	defer restore()

	err := MoveWithAPI(writeTestConfig(t), api)
	if err == nil {
		t.Fatal("expected error for empty supported-items list")
	}
	if !strings.Contains(err.Error(), "no reports, semantic models, or notebooks") {
		t.Errorf("expected friendly empty-state error, got: %v", err)
	}
}

func TestMove_SingleWorkspaceInTenant_NothingToMoveTo(t *testing.T) {
	api := &fakeMoveAPI{
		token:      "fake",
		workspaces: []fabric.Workspace{{ID: "ws-a", DisplayName: "DW - DEV"}},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "nb-1", DisplayName: "LoadHR", Type: "Notebook", WorkspaceID: "ws-a"}},
		},
	}
	restore := withMovePickers(t, nil, nil, "")
	defer restore()

	err := MoveWithAPI(writeTestConfig(t), api)
	if err == nil {
		t.Fatal("expected error for single-workspace tenant")
	}
	if !strings.Contains(err.Error(), "nothing to move to") {
		t.Errorf("expected 'nothing to move to' error, got: %v", err)
	}
}

func TestMove_CreateItemFails_MoveErrSurfaces(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {{ID: "nb-1", DisplayName: "LoadHR", Type: "Notebook", WorkspaceID: "ws-a"}},
			"ws-b": {},
		},
		createErr: errors.New("403 Forbidden"),
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "LoadHR", "DW - feature"},
		nil,
		"")
	defer restore()

	// executeMove prints the error box and returns nil — partial/full
	// failure are statuses, not Go errors, matching the run flow's
	// pattern. The test confirms create was attempted and rebind was
	// not (it's a notebook anyway, but defensive).
	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI returned error (should be nil — failure is a status): %v", err)
	}
	if api.createCalls != 1 {
		t.Errorf("expected 1 create call (which failed), got %d", api.createCalls)
	}
	if api.rebindCalls != 0 {
		t.Errorf("rebind must not be called when create failed, got %d", api.rebindCalls)
	}
}

func TestMove_RebindToModelInDifferentWorkspace(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
			{ID: "ws-shared", DisplayName: "DW - Shared Models"},
		},
		items: map[string][]fabric.Item{
			"ws-a":      {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-b":      {}, // destination has NO semantic models
			"ws-shared": {{ID: "sm-canonical", DisplayName: "Canonical HR", Type: "SemanticModel", WorkspaceID: "ws-shared"}},
		},
	}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "HR", "DW - feature", "Canonical HR"},
		nil,
		"")
	defer restore()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.rebindCalls != 1 {
		t.Errorf("expected 1 rebind call, got %d", api.rebindCalls)
	}
	if api.lastRebindDataset != "sm-canonical" {
		t.Errorf("expected rebind to sm-canonical from shared workspace, got %q", api.lastRebindDataset)
	}
}

// TestMove_RebindPicker_DestinationModelsFirst asserts the rebind picker
// surfaces the destination workspace's semantic models at the top of the
// list (right after Skip), even when the destination is listed last among
// all workspaces. Cross-workspace rebind stays available — destination-first
// is just the ordering, since after a move you almost always rebind to a
// model that lives in the destination.
func TestMove_RebindPicker_DestinationModelsFirst(t *testing.T) {
	api := &fakeMoveAPI{
		token: "fake",
		// Destination (ws-b "feature/jane") is listed LAST on purpose: the
		// old behaviour iterated workspaces in order, so its models came last.
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-shared", DisplayName: "DW - Shared Models"},
			{ID: "ws-b", DisplayName: "feature/jane"},
		},
		items: map[string][]fabric.Item{
			"ws-a":      {{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"}},
			"ws-shared": {{ID: "sm-shared", DisplayName: "Shared Model", Type: "SemanticModel", WorkspaceID: "ws-shared"}},
			"ws-b":      {{ID: "sm-dest", DisplayName: "Dest Model", Type: "SemanticModel", WorkspaceID: "ws-b"}},
		},
	}

	// withMovePickers installs the save/restore plus the number/prompt/confirm
	// stubs; we override only the filter picker to capture the rebind option
	// order (and bail out via Skip).
	restore := withMovePickers(t, nil, nil, "")
	defer restore()

	moveMultiPicker = multiPickByNames("HR")
	picks := []string{"DW - DEV", "feature/jane"} // source ws, destination ws
	pickIdx := 0
	var rebindOptions []ui.FilterOption
	moveFilterPicker = func(_ string, options []ui.FilterOption, _ ui.FilterRowRenderer) (string, error) {
		// The rebind picker is the only one carrying the Skip sentinel —
		// capture its option order and bail out via Skip.
		for _, o := range options {
			if o.Value == rebindSkip {
				rebindOptions = options
				return rebindSkip, nil
			}
		}
		if pickIdx >= len(picks) {
			return "", fmt.Errorf("unexpected filter picker call beyond %d picks", len(picks))
		}
		target := picks[pickIdx]
		pickIdx++
		for _, o := range options {
			if o.Label == target {
				return o.Value, nil
			}
		}
		return "", fmt.Errorf("no option labeled %q", target)
	}

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}

	if len(rebindOptions) < 3 {
		t.Fatalf("expected Skip + two models, got %d options", len(rebindOptions))
	}
	if rebindOptions[0].Value != rebindSkip {
		t.Errorf("expected Skip first, got %q", rebindOptions[0].Label)
	}
	first := rebindOptions[1]
	wsName, _ := first.Meta.(string)
	if wsName != "feature/jane" {
		t.Errorf("expected destination workspace's model first, got Label=%q (workspace %q)", first.Label, wsName)
	}
	if first.Label != "Dest Model" {
		t.Errorf("expected 'Dest Model' first, got %q", first.Label)
	}
}

func (f *fakeMoveAPI) SetVariableLibraryActiveSet(token, ws, id, valueSetName string) error {
	return nil
}

func (f *fakeMoveAPI) PublishEnvironment(token, ws, id string) error { return nil }
func (f *fakeMoveAPI) GetEnvironmentPublishState(token, ws, id string) (string, error) {
	return "success", nil
}
func (f *fakeMoveAPI) ListFolders(string, string) ([]fabric.Folder, error) { return nil, nil }
func (f *fakeMoveAPI) CreateFolder(_, _, name, parentID string) (fabric.Folder, error) {
	return fabric.Folder{ID: "fld-" + name, DisplayName: name, ParentFolderID: parentID}, nil
}

// Item browser methods — not used by move tests.
func (f *fakeMoveAPI) RenameItem(string, string, string, string) (fabric.Item, error) {
	return fabric.Item{}, errors.New("RenameItem not used by move tests")
}
func (f *fakeMoveAPI) SetItemDescription(string, string, string, string) (fabric.Item, error) {
	return fabric.Item{}, errors.New("SetItemDescription not used by move tests")
}

func twoNotebookTenant() *fakeMoveAPI {
	return &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {
				{ID: "nb-1", DisplayName: "LoadHR", Type: "Notebook", WorkspaceID: "ws-a"},
				{ID: "nb-2", DisplayName: "LoadSales", Type: "Notebook", WorkspaceID: "ws-a"},
			},
			"ws-b": {},
		},
	}
}

func TestMove_MultipleItems_EachCopiedToDestination(t *testing.T) {
	api := twoNotebookTenant()
	restore := withMovePickers(t, []string{"DW - DEV", "DW - feature"}, nil, "")
	defer restore()
	moveMultiPicker = multiPickByNames("LoadHR", "LoadSales")

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if got := strings.Join(api.createNames, ","); got != "LoadHR,LoadSales" {
		t.Errorf("expected both notebooks created in order, got %q", got)
	}
}

func TestMove_CollisionSkip_LeavesOtherItemsMoving(t *testing.T) {
	api := twoNotebookTenant()
	api.items["ws-b"] = []fabric.Item{{ID: "nb-old", DisplayName: "LoadHR", Type: "Notebook", WorkspaceID: "ws-b"}}
	restore := withMovePickers(t,
		[]string{"DW - DEV", "DW - feature"},
		[]string{"Skip this item"}, // collision menu for LoadHR
		"")
	defer restore()
	moveMultiPicker = multiPickByNames("LoadHR", "LoadSales")

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if got := strings.Join(api.createNames, ","); got != "LoadSales" {
		t.Errorf("expected only LoadSales created, got %q", got)
	}
	if api.updateCalls != 0 {
		t.Errorf("skip must not overwrite, got %d updates", api.updateCalls)
	}
}

func TestMove_OneCreateFails_RemainingItemsStillMoved(t *testing.T) {
	api := twoNotebookTenant()
	api.failCreateNames = map[string]bool{"LoadHR": true}
	restore := withMovePickers(t, []string{"DW - DEV", "DW - feature"}, nil, "")
	defer restore()
	moveMultiPicker = multiPickByNames("LoadHR", "LoadSales")

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("a failed item is reported, not returned: %v", err)
	}
	if got := strings.Join(api.createNames, ","); got != "LoadHR,LoadSales" {
		t.Errorf("expected LoadSales attempted after LoadHR failed, got %q", got)
	}
}

func TestMove_NothingChecked_NothingMoved(t *testing.T) {
	api := twoNotebookTenant()
	restore := withMovePickers(t, []string{"DW - DEV"}, nil, "")
	defer restore()
	moveMultiPicker = multiPickByNames()

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("empty selection is a no-op, got %v", err)
	}
	if api.createCalls != 0 {
		t.Errorf("nothing checked must create nothing, got %d creates", api.createCalls)
	}
}

func twoReportTenant() *fakeMoveAPI {
	return &fakeMoveAPI{
		token: "fake",
		workspaces: []fabric.Workspace{
			{ID: "ws-a", DisplayName: "DW - DEV"},
			{ID: "ws-b", DisplayName: "DW - feature"},
		},
		items: map[string][]fabric.Item{
			"ws-a": {
				{ID: "r-1", DisplayName: "HR", Type: "Report", WorkspaceID: "ws-a"},
				{ID: "r-2", DisplayName: "Sales", Type: "Report", WorkspaceID: "ws-a"},
			},
			"ws-b": {
				{ID: "sm-1", DisplayName: "HR-Model", Type: "SemanticModel", WorkspaceID: "ws-b"},
				{ID: "sm-2", DisplayName: "Sales-Model", Type: "SemanticModel", WorkspaceID: "ws-b"},
			},
		},
	}
}

func TestMove_TwoReports_OneRebindPickAppliesToBoth(t *testing.T) {
	api := twoReportTenant()
	restore := withMovePickers(t,
		[]string{"DW - DEV", "DW - feature", "HR-Model"}, // src ws, dst ws, default model
		[]string{"Continue with this plan"},              // rebind plan menu
		"")
	defer restore()
	moveMultiPicker = multiPickQueue([]string{"HR", "Sales"})

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.rebindByReport["new-HR"] != "sm-1" || api.rebindByReport["new-Sales"] != "sm-1" {
		t.Errorf("both reports should rebind to sm-1, got %v", api.rebindByReport)
	}
}

func TestMove_TwoReports_ChangeModelForOne(t *testing.T) {
	api := twoReportTenant()
	restore := withMovePickers(t,
		[]string{"DW - DEV", "DW - feature", "HR-Model", "Sales-Model"}, // ..., default model, model for the changed row
		[]string{"Change model for some reports", "Continue with this plan"},
		"")
	defer restore()
	moveMultiPicker = multiPickQueue([]string{"HR", "Sales"}, []string{"Sales"})

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if got := api.rebindByReport; got["new-HR"] != "sm-1" || got["new-Sales"] != "sm-2" {
		t.Errorf("HR should keep the default sm-1 and Sales move to sm-2, got %v", got)
	}
}

func TestMove_TwoReports_ChangeToSkipLeavesBinding(t *testing.T) {
	api := twoReportTenant()
	restore := withMovePickers(t,
		[]string{"DW - DEV", "DW - feature", "HR-Model", "⋯ Skip (keep current binding)"},
		[]string{"Change model for some reports", "Continue with this plan"},
		"")
	defer restore()
	moveMultiPicker = multiPickQueue([]string{"HR", "Sales"}, []string{"HR"})

	if err := MoveWithAPI(writeTestConfig(t), api); err != nil {
		t.Fatalf("MoveWithAPI: %v", err)
	}
	if api.rebindCalls != 1 || api.rebindByReport["new-Sales"] != "sm-1" {
		t.Errorf("only Sales should rebind, got calls=%d map=%v", api.rebindCalls, api.rebindByReport)
	}
}

func TestMoveWriteErrorOnlyBlamesPermissionsWhenItIsPermissions(t *testing.T) {
	// The hint was appended to every failed write. A Fabric conversion failure
	// names its own cause perfectly well, and telling the user to go check
	// access rights that were already fine sends them the wrong way entirely.
	conversion := errors.New(`operation failed: {"errorCode":"PyToIPynbFailure","message":"Convert py to ipynb failed"}`)
	got := moveWriteError("create item", conversion, "DW - PROD - Config").Error()
	if strings.Contains(got, "Member or higher") {
		t.Errorf("a conversion failure must not blame permissions:\n%s", got)
	}
	if !strings.Contains(got, "PyToIPynbFailure") {
		t.Errorf("the real cause must survive:\n%s", got)
	}

	for _, denial := range []error{
		errors.New("create item 403: InsufficientPrivileges"),
		errors.New("update item 401: Unauthorized"),
		errors.New("needs read and write permission on the item"),
	} {
		got := moveWriteError("create item", denial, "Target WS").Error()
		if !strings.Contains(got, "Member or higher") {
			t.Errorf("a denial must carry the hint:\n%s", got)
		}
		if !strings.Contains(got, "Target WS") {
			t.Errorf("the hint must name the destination:\n%s", got)
		}
	}
}
