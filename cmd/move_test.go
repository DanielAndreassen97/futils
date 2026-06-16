package cmd

import (
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
	lastUpdateItemID  string
	lastRebindDataset string
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
func (f *fakeMoveAPI) CreateItem(_, ws, name, typ string, _ *fabric.Definition) (fabric.Item, error) {
	f.createCalls++
	f.lastCreateName = name
	if f.createErr != nil {
		return fabric.Item{}, f.createErr
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
func (f *fakeMoveAPI) RebindReport(_, _, _, datasetID string) error {
	f.rebindCalls++
	f.lastRebindDataset = datasetID
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

// withMovePickers installs deterministic pickers for the duration
// of a single test. The filter picker picks the option whose Label
// matches filterPick (or first option if filterPick is empty); the
// number picker is driven by a queue of pre-set values that match
// each menu in order. promptInput just returns promptReturn.
func withMovePickers(t *testing.T, filterPicks []string, numberPicks []string, promptReturn string) func() {
	t.Helper()
	origFilter := moveFilterPicker
	origNumber := moveNumberPicker
	origPrompt := movePromptInput
	origConfirm := moveConfirm

	filterIdx := 0
	numberIdx := 0
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
		moveNumberPicker = origNumber
		movePromptInput = origPrompt
		moveConfirm = origConfirm
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

	picks := []string{"DW - DEV", "HR", "feature/jane"} // source ws, item, destination ws
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
