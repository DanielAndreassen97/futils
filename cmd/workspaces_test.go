package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// ── pure rendering helpers ─────────────────────────────────────────────────

func TestItemTypeCountsSortsByCountThenName(t *testing.T) {
	items := []fabric.Item{
		{Type: "Report"}, {Type: "Report"},
		{Type: "Notebook"}, {Type: "Notebook"}, {Type: "Notebook"},
		{Type: "Lakehouse"}, {Type: "Lakehouse"},
	}
	// Report and Lakehouse tie at 2, so the name breaks the tie — the biggest
	// group must read first because this line is what a delete is judged on.
	if got, want := itemTypeCounts(items), "3 Notebook, 2 Lakehouse, 2 Report"; got != want {
		t.Errorf("itemTypeCounts = %q, want %q", got, want)
	}
}

func TestItemTypeCountsEmpty(t *testing.T) {
	if got := itemTypeCounts(nil); got != "none" {
		t.Errorf("itemTypeCounts(nil) = %q, want none", got)
	}
}

func TestCapacityLabel(t *testing.T) {
	caps := []fabric.Capacity{
		{ID: "cap-1", DisplayName: "Prod F64", SKU: "F64", Region: "Norway East", State: "Active"},
	}
	if got, want := capacityLabel(caps, "cap-1"), "Prod F64 · F64 · Norway East"; got != want {
		t.Errorf("capacityLabel = %q, want %q", got, want)
	}
	if got := capacityLabel(caps, ""); got != "none" {
		t.Errorf("capacityLabel with no capacity = %q, want none", got)
	}
	// A capacity the caller can't see must not render as "none" — the workspace
	// does have one, we just can't name it.
	got := capacityLabel(caps, "cap-unknown")
	if !strings.Contains(got, "cap-unknown") || got == "none" {
		t.Errorf("capacityLabel for an invisible capacity = %q", got)
	}
	if !strings.Contains(got, "no access") {
		t.Errorf("an unresolvable id in a readable list = %q, want a no-access note", got)
	}
	// When the whole list failed to load, blaming access is wrong — we never
	// looked.
	got = capacityLabel(nil, "cap-1")
	if strings.Contains(got, "no access") {
		t.Errorf("capacityLabel with no list = %q, must not claim missing access", got)
	}
	if !strings.Contains(got, "cap-1") {
		t.Errorf("capacityLabel with no list = %q, want the raw id", got)
	}
}

func TestRenderWorkspaceRefsCoversEachKind(t *testing.T) {
	refs := []config.WorkspaceRef{
		{Customer: "Fabrikam", Environment: "DEV", Kind: config.RefEnvWorkspace},
		{Customer: "Fabrikam", Environment: "TEST", Kind: config.RefDeployTarget, Mapping: "Backend"},
		{Customer: "Contoso", Environment: "PROD", Kind: config.RefDeployBaseline, Mapping: "Shared"},
	}
	got := renderWorkspaceRefs(refs)
	for _, want := range []string{"Fabrikam / DEV", "Fabrikam / TEST", "Contoso / PROD", "Backend", "Shared", "baseline"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered refs missing %q:\n%s", want, got)
		}
	}
}

func TestRenderWorkspaceRefsEmpty(t *testing.T) {
	if got := renderWorkspaceRefs(nil); got != "" {
		t.Errorf("renderWorkspaceRefs(nil) = %q, want empty", got)
	}
}

// ── validation ────────────────────────────────────────────────────────────

func TestValidateWorkspaceNameRejections(t *testing.T) {
	existing := []fabric.Workspace{
		{ID: "ws-1", DisplayName: "DW - Finance"},
		{ID: "ws-2", DisplayName: "DW - SemMod"},
	}
	cases := []struct {
		name    string
		input   string
		current string
		want    string // substring the error must contain
	}{
		{"empty", "", "", "empty"},
		{"whitespace only", "   ", "", "empty"},
		{"too long", strings.Repeat("x", fabric.MaxWorkspaceNameLen+1), "", "256"},
		{"reserved", "Admin monitoring", "", "reserved"},
		{"reserved other case", "admin MONITORING", "", "reserved"},
		{"already taken", "DW - SemMod", "", "already"},
		{"unchanged during rename", "DW - Finance", "DW - Finance", "differ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateWorkspaceName(c.input, existing, c.current)
			if err == nil {
				t.Fatalf("validateWorkspaceName(%q) = nil, want an error", c.input)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
				t.Errorf("error %q must mention %q", err, c.want)
			}
		})
	}
}

func TestValidateWorkspaceNameAccepts(t *testing.T) {
	existing := []fabric.Workspace{{ID: "ws-1", DisplayName: "DW - Finance"}}

	if err := validateWorkspaceName("DW - Sales", existing, ""); err != nil {
		t.Errorf("a fresh name must be accepted, got %v", err)
	}
	// Renaming to a name that only collides with the workspace being renamed is
	// impossible — that case is the "must differ" error, tested above. Renaming
	// past its own old name to something new must pass.
	if err := validateWorkspaceName("DW - Sales", existing, "DW - Finance"); err != nil {
		t.Errorf("rename to a fresh name must be accepted, got %v", err)
	}
	if err := validateWorkspaceName(strings.Repeat("x", fabric.MaxWorkspaceNameLen), existing, ""); err != nil {
		t.Errorf("a name at the length limit must be accepted, got %v", err)
	}
}

func TestValidateWorkspaceDescription(t *testing.T) {
	if err := validateWorkspaceDescription(""); err != nil {
		t.Errorf("an empty description must be allowed, got %v", err)
	}
	if err := validateWorkspaceDescription(strings.Repeat("x", fabric.MaxWorkspaceDescLen)); err != nil {
		t.Errorf("a description at the limit must be allowed, got %v", err)
	}
	err := validateWorkspaceDescription(strings.Repeat("x", fabric.MaxWorkspaceDescLen+1))
	if err == nil || !strings.Contains(err.Error(), "4000") {
		t.Errorf("over-long description error = %v, want a mention of 4000", err)
	}
}

// ── flow ──────────────────────────────────────────────────────────────────

// wsFakeAPI is the workspace-management test double. It records the mutating
// calls so tests can assert both that the right call was made and that config
// was only written afterwards.
type wsFakeAPI struct {
	APIClient // embedded nil: any unexpected call panics, making the gap obvious

	workspaces []fabric.Workspace
	roleOf     map[string]string // workspaceID -> Admin/Member/Contributor/Viewer
	items      map[string][]fabric.Item
	capacities []fabric.Capacity

	created   []string // displayNames passed to CreateWorkspace
	renamed   [][2]string
	deleted   []string
	descSet   [][2]string
	createErr error
	renameErr error
	deleteErr error
}

func (f *wsFakeAPI) GetAccessToken(string) (string, error) { return "tok", nil }
func (f *wsFakeAPI) ListWorkspaces(string) ([]fabric.Workspace, error) {
	return f.workspaces, nil
}

// ListWorkspacesByRole honours the roles argument, the way Fabric does — the
// flow makes one call per role to build its role map, so a fake that ignored
// the filter would let a broken mapping pass.
func (f *wsFakeAPI) ListWorkspacesByRole(_, roles string) ([]fabric.Workspace, error) {
	var out []fabric.Workspace
	for _, ws := range f.workspaces {
		if f.roleOf[ws.ID] == roles {
			out = append(out, ws)
		}
	}
	return out, nil
}
func (f *wsFakeAPI) GetWorkspace(_, id string) (fabric.Workspace, error) {
	for _, ws := range f.workspaces {
		if ws.ID == id {
			return ws, nil
		}
	}
	return fabric.Workspace{}, errors.New("not found")
}
func (f *wsFakeAPI) ListItems(_, id string) ([]fabric.Item, error) { return f.items[id], nil }
func (f *wsFakeAPI) ListCapacities(string) ([]fabric.Capacity, error) {
	return f.capacities, nil
}
func (f *wsFakeAPI) CreateWorkspace(_, name, desc, capID string) (fabric.Workspace, error) {
	if f.createErr != nil {
		return fabric.Workspace{}, f.createErr
	}
	f.created = append(f.created, name)
	ws := fabric.Workspace{ID: "new-ws", DisplayName: name, Description: desc, CapacityID: capID}
	f.workspaces = append(f.workspaces, ws)
	return ws, nil
}
func (f *wsFakeAPI) RenameWorkspace(_, id, name string) (fabric.Workspace, error) {
	if f.renameErr != nil {
		return fabric.Workspace{}, f.renameErr
	}
	f.renamed = append(f.renamed, [2]string{id, name})
	return fabric.Workspace{ID: id, DisplayName: name}, nil
}
func (f *wsFakeAPI) SetWorkspaceDescription(_, id, desc string) (fabric.Workspace, error) {
	f.descSet = append(f.descSet, [2]string{id, desc})
	return fabric.Workspace{ID: id, Description: desc}, nil
}
func (f *wsFakeAPI) DeleteWorkspace(_, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// wsHarness drives the flow's prompts deterministically. Picks are consumed in
// order; running out returns ErrGoBack, which unwinds the flow the same way a
// user pressing esc would — that is how these tests terminate.
type wsHarness struct {
	filterPicks []string
	numberPicks []string
	inputs      []string
	confirms    []bool
	typedOK     []bool

	confirmPrompts []string
	typedPrompts   []string
	typedWords     []string
	inputPrompts   []string
}

func (h *wsHarness) install(t *testing.T) {
	t.Helper()
	origFilter, origNumber, origInput := wsFilterPicker, wsNumberPicker, wsPromptInput
	origConfirm, origTyped := wsConfirm, wsConfirmTyped
	t.Cleanup(func() {
		wsFilterPicker, wsNumberPicker, wsPromptInput = origFilter, origNumber, origInput
		wsConfirm, wsConfirmTyped = origConfirm, origTyped
	})

	wsFilterPicker = func(_ string, _ []ui.FilterOption, _ ui.FilterRowRenderer) (string, error) {
		if len(h.filterPicks) == 0 {
			return "", ui.ErrGoBack
		}
		pick := h.filterPicks[0]
		h.filterPicks = h.filterPicks[1:]
		return pick, nil
	}
	wsNumberPicker = func(_ string, _ []ui.MenuOption) (string, error) {
		if len(h.numberPicks) == 0 {
			return "", ui.ErrGoBack
		}
		pick := h.numberPicks[0]
		h.numberPicks = h.numberPicks[1:]
		return pick, nil
	}
	wsPromptInput = func(title, _ string) (string, error) {
		h.inputPrompts = append(h.inputPrompts, title)
		if len(h.inputs) == 0 {
			return "", ui.ErrGoBack
		}
		in := h.inputs[0]
		h.inputs = h.inputs[1:]
		return in, nil
	}
	wsConfirm = func(msg string) (bool, error) {
		h.confirmPrompts = append(h.confirmPrompts, msg)
		if len(h.confirms) == 0 {
			return false, nil
		}
		c := h.confirms[0]
		h.confirms = h.confirms[1:]
		return c, nil
	}
	wsConfirmTyped = func(msg, word string) (bool, error) {
		h.typedPrompts = append(h.typedPrompts, msg)
		h.typedWords = append(h.typedWords, word)
		if len(h.typedOK) == 0 {
			return false, nil
		}
		ok := h.typedOK[0]
		h.typedOK = h.typedOK[1:]
		return ok, nil
	}
}

// wsConfigFile writes a one-customer config holding refs to "DW - Finance" in
// all three places and returns its path.
func wsConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Customers: map[string]config.Customer{
		"Fabrikam": {
			Environments: []config.Environment{
				{Alias: "DEV", Workspaces: []string{"DW - Finance", "DW - SemMod"}},
				{Alias: "TEST", Workspaces: []string{"DW - SemMod"}, Deployments: []config.DeployMapping{
					{Folder: "Backend", Workspace: "DW - Finance"},
					{Folder: "Shared", Workspace: "DW - SemMod", BaselineWorkspace: "DW - Finance"},
				}},
			},
		},
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func wsTestAPI() *wsFakeAPI {
	return &wsFakeAPI{
		workspaces: []fabric.Workspace{
			{ID: "ws-fin", DisplayName: "DW - Finance", Description: "Finance data", CapacityID: "cap-1"},
			{ID: "ws-sem", DisplayName: "DW - SemMod"},
		},
		roleOf: map[string]string{"ws-fin": "Admin", "ws-sem": "Viewer"},
		items: map[string][]fabric.Item{
			"ws-fin": {{Type: "Notebook"}, {Type: "Notebook"}, {Type: "Lakehouse"}},
		},
		capacities: []fabric.Capacity{
			{ID: "cap-1", DisplayName: "Prod F64", SKU: "F64", Region: "Norway East", State: "Active"},
		},
	}
}

func TestWorkspacesRenameRewritesConfigReferences(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionRename},
		inputs:      []string{"DW - Finance PROD"},
		confirms:    []bool{true},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.renamed) != 1 || api.renamed[0] != [2]string{"ws-fin", "DW - Finance PROD"} {
		t.Fatalf("renamed = %v", api.renamed)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance"); len(refs) != 0 {
		t.Errorf("old name still referenced: %+v", refs)
	}
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance PROD"); len(refs) != 3 {
		t.Errorf("new name has %d references, want 3", len(refs))
	}
	// The user must see the impact before confirming.
	if !strings.Contains(out, "Backend") {
		t.Errorf("output must list the affected deploy mapping:\n%s", out)
	}
}

func TestWorkspacesRenameDeclinedTouchesNothing(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionRename},
		inputs:      []string{"DW - Finance PROD"},
		confirms:    []bool{false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.renamed) != 0 {
		t.Errorf("declining must not call Fabric, got %v", api.renamed)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance"); len(refs) != 3 {
		t.Errorf("config changed after a declined rename: %+v", refs)
	}
}

func TestWorkspacesRenameAPIFailureLeavesConfigUntouched(t *testing.T) {
	// Config must never claim a name Fabric does not have.
	path := wsConfigFile(t)
	api := wsTestAPI()
	api.renameErr = errors.New("update workspace 403: requires the Admin workspace role")
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionRename},
		inputs:      []string{"DW - Finance PROD"},
		confirms:    []bool{true},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err == nil {
			t.Fatal("expected the API error to surface")
		}
	})

	cfg, _ := config.Load(path)
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance"); len(refs) != 3 {
		t.Errorf("config changed despite the rename failing: %+v", refs)
	}
}

func TestWorkspacesDeleteRequiresTypedYesAndCleansConfig(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionDelete},
		typedOK:     []bool{true},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.deleted) != 1 || api.deleted[0] != "ws-fin" {
		t.Fatalf("deleted = %v", api.deleted)
	}
	if len(h.typedWords) != 1 || h.typedWords[0] != "Yes" {
		t.Errorf("the typed confirmation word must be Yes, got %v", h.typedWords)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance"); len(refs) != 0 {
		t.Errorf("config still references the deleted workspace: %+v", refs)
	}
	// The blast radius has to be on screen before the prompt.
	for _, want := range []string{"2 Notebook", "Backend"} {
		if !strings.Contains(out, want) {
			t.Errorf("delete output missing %q:\n%s", want, out)
		}
	}
}

func TestWorkspacesDeleteWithoutTypedYesAborts(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionDelete},
		typedOK:     []bool{false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.deleted) != 0 {
		t.Errorf("delete must not run without the typed word, got %v", api.deleted)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindWorkspaceRefs(cfg, "DW - Finance"); len(refs) != 3 {
		t.Errorf("config changed after an aborted delete: %+v", refs)
	}
}

func TestWorkspacesNonAdminCannotReachTheAPI(t *testing.T) {
	// ws-sem is not in adminIDs. Both PATCH and DELETE need the Admin role, so
	// the flow must refuse before spending a call on a guaranteed 403.
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-sem"},
		numberPicks: []string{wsActionDelete},
		typedOK:     []bool{true},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.deleted) != 0 {
		t.Errorf("a non-admin must not reach DeleteWorkspace, got %v", api.deleted)
	}
	if len(h.typedPrompts) != 0 {
		t.Errorf("a non-admin must not even be asked to confirm, got %v", h.typedPrompts)
	}
	if !strings.Contains(out, "Admin") {
		t.Errorf("output must explain the missing Admin role:\n%s", out)
	}
}

func TestWorkspacesCreateRegistersInEnvironment(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{wsActionCreate, "cap-1"},
		inputs:      []string{"DW - Sales", "Sales data"},
		// confirm 1: create summary. confirm 2: add to the customer's config.
		confirms:    []bool{true, true},
		numberPicks: []string{"DEV"},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.created) != 1 || api.created[0] != "DW - Sales" {
		t.Fatalf("created = %v", api.created)
	}
	cfg, _ := config.Load(path)
	refs := config.FindWorkspaceRefs(cfg, "DW - Sales")
	if len(refs) != 1 || refs[0].Environment != "DEV" || refs[0].Kind != config.RefEnvWorkspace {
		t.Errorf("new workspace not registered in DEV: %+v", refs)
	}
}

func TestWorkspacesCreateDeclinedRegistrationLeavesConfigAlone(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{wsActionCreate, wsCapacitySkip},
		inputs:      []string{"DW - Sales", ""},
		confirms:    []bool{true, false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.created) != 1 {
		t.Fatalf("created = %v", api.created)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindWorkspaceRefs(cfg, "DW - Sales"); len(refs) != 0 {
		t.Errorf("config was written despite declining: %+v", refs)
	}
}

func TestWorkspacesCreateRejectsDuplicateNameBeforeCallingFabric(t *testing.T) {
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{wsActionCreate},
		inputs:      []string{"DW - SemMod"},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.created) != 0 {
		t.Errorf("a duplicate name must not reach Fabric, got %v", api.created)
	}
	if !strings.Contains(strings.ToLower(out), "already") {
		t.Errorf("output must say the name is taken:\n%s", out)
	}
}

func TestWorkspacesNoCustomersIsFriendly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"customers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, wsTestAPI()); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})
	if !strings.Contains(out, "No customers configured") {
		t.Errorf("output = %q", out)
	}
}

// TestWorkspacesAgainstDemoTenant walks create → rename → delete against the
// demo client, the path a `FUTILS_DEMO=1` session takes. The unit tests above
// use a purpose-built fake; this one proves the demo tenant actually mutates,
// which is what a demo or a recorded walkthrough depends on.
func TestWorkspacesAgainstDemoTenant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeDemoConfig(path, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	api := newDemoClient()

	before, err := api.ListWorkspaces("tok")
	if err != nil {
		t.Fatal(err)
	}

	// Create, declining the config registration to keep the assertions on Fabric.
	h := &wsHarness{
		filterPicks: []string{wsActionCreate, demoCapacityID},
		inputs:      []string{"DW - Smoke", "created by a test"},
		confirms:    []bool{true, false},
	}
	h.install(t)
	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("create: %v", err)
		}
	})

	created, ok := demoWorkspaceNamed(api, "DW - Smoke")
	if !ok {
		t.Fatal("the demo tenant did not gain the created workspace")
	}
	if created.CapacityID != demoCapacityID {
		t.Errorf("created workspace capacity = %q, want the demo capacity", created.CapacityID)
	}

	// Rename it.
	h = &wsHarness{
		filterPicks: []string{created.ID},
		numberPicks: []string{wsActionRename},
		inputs:      []string{"DW - Smoke renamed"},
		confirms:    []bool{true},
	}
	h.install(t)
	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("rename: %v", err)
		}
	})
	if _, ok := demoWorkspaceNamed(api, "DW - Smoke renamed"); !ok {
		t.Fatal("the demo tenant did not apply the rename")
	}

	// Delete it.
	h = &wsHarness{
		filterPicks: []string{created.ID},
		numberPicks: []string{wsActionDelete},
		typedOK:     []bool{true},
	}
	h.install(t)
	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	after, err := api.ListWorkspaces("tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("demo tenant has %d workspaces after the round trip, want the original %d", len(after), len(before))
	}
}

func demoWorkspaceNamed(api *demoClient, name string) (fabric.Workspace, bool) {
	all, err := api.ListWorkspaces("tok")
	if err != nil {
		return fabric.Workspace{}, false
	}
	for _, ws := range all {
		if ws.DisplayName == name {
			return ws, true
		}
	}
	return fabric.Workspace{}, false
}

func TestWorkspacesPanelNamesTheCapacityOnFirstVisit(t *testing.T) {
	// Capacities are session state. If they are only fetched when creating, the
	// very first detail panel renders a raw GUID and blames it on access the
	// user actually has.
	path := wsConfigFile(t)
	api := wsTestAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin"},
		numberPicks: []string{wsActionBack},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if !strings.Contains(out, "Prod F64 · F64 · Norway East") {
		t.Errorf("panel must name the capacity on the first visit:\n%s", out)
	}
	if strings.Contains(out, "no access to this capacity") {
		t.Errorf("panel wrongly claims the capacity is inaccessible:\n%s", out)
	}
}

func TestGroupWorkspacesByRoleOrdersGroupsByPower(t *testing.T) {
	workspaces := []fabric.Workspace{
		{ID: "v1", DisplayName: "Viewer one"},
		{ID: "a1", DisplayName: "Admin one"},
		{ID: "c1", DisplayName: "Contributor one"},
		{ID: "m1", DisplayName: "Member one"},
		{ID: "o1", DisplayName: "My workspace"},
	}
	roleOf := map[string]string{
		"v1": "Viewer", "a1": "Admin", "c1": "Contributor", "m1": "Member",
	}

	opts := groupWorkspacesByRole(workspaces, roleOf, nil)

	var headers []string
	for _, o := range opts {
		if o.IsHeader {
			headers = append(headers, o.Label)
		}
	}
	want := []string{"ADMIN · 1", "MEMBER · 1", "CONTRIBUTOR · 1", "VIEWER · 1", "NO WORKSPACE ROLE · 1"}
	if len(headers) != len(want) {
		t.Fatalf("headers = %v, want %v", headers, want)
	}
	for i := range want {
		if headers[i] != want[i] {
			t.Fatalf("headers = %v, want %v", headers, want)
		}
	}
}

func TestGroupWorkspacesByRoleOmitsEmptyGroups(t *testing.T) {
	workspaces := []fabric.Workspace{{ID: "a1", DisplayName: "Only admin"}}
	opts := groupWorkspacesByRole(workspaces, map[string]string{"a1": "Admin"}, nil)

	for _, o := range opts {
		if o.IsHeader && o.Label != "ADMIN · 1" {
			t.Errorf("unexpected header %q — empty groups must be omitted", o.Label)
		}
	}
	if len(opts) != 2 {
		t.Errorf("got %d rows, want a header plus one workspace", len(opts))
	}
}

func TestGroupWorkspacesByRoleSortsInsideAGroup(t *testing.T) {
	// Case-insensitive: "apple" must not sort after "Zebra" just because of
	// where the ASCII table puts lowercase letters.
	workspaces := []fabric.Workspace{
		{ID: "1", DisplayName: "Zebra"},
		{ID: "2", DisplayName: "apple"},
		{ID: "3", DisplayName: "Mango"},
	}
	roleOf := map[string]string{"1": "Admin", "2": "Admin", "3": "Admin"}

	opts := groupWorkspacesByRole(workspaces, roleOf, nil)

	var names []string
	for _, o := range opts {
		if !o.IsHeader {
			names = append(names, o.Label)
		}
	}
	want := []string{"apple", "Mango", "Zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestGroupWorkspacesByRoleCarriesTheTierInMeta(t *testing.T) {
	// The renderer colours the row from Meta, so the tier has to travel with
	// the option rather than being recomputed at render time.
	workspaces := []fabric.Workspace{{ID: "a1", DisplayName: "With capacity", Type: "Workspace", CapacityID: "cap-1"}}
	caps := []fabric.Capacity{{ID: "cap-1", DisplayName: "Prod F64", SKU: "F64", Region: "Norway East"}}

	opts := groupWorkspacesByRole(workspaces, map[string]string{"a1": "Admin"}, caps)

	for _, o := range opts {
		if o.IsHeader {
			continue
		}
		tier, ok := o.Meta.(workspaceTier)
		if !ok {
			t.Fatalf("row meta is %T, want a workspaceTier", o.Meta)
		}
		if tier.Name != "Fabric" || tier.SKU != "F64" {
			t.Errorf("tier = %q/%q, want Fabric/F64", tier.Name, tier.SKU)
		}
	}
}

func TestWorkspacesPanelNamesTheLicenceTier(t *testing.T) {
	// "no capacity" told you what futils failed to find. "Pro" tells you how
	// the workspace is actually licensed.
	path := wsConfigFile(t)
	api := wsTestAPI()
	api.workspaces[1].Type = "Workspace" // ws-sem has no capacity — a Pro workspace
	h := &wsHarness{
		filterPicks: []string{"ws-sem"},
		numberPicks: []string{wsActionBack},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if !strings.Contains(out, "Pro") {
		t.Errorf("panel must name the licence tier:\n%s", out)
	}
	if strings.Contains(out, "no capacity") {
		t.Errorf("panel still says 'no capacity':\n%s", out)
	}
}

func TestWorkspacesPanelShowsTheConcreteRole(t *testing.T) {
	// "read-only" hides which role you actually hold. Contributor and Viewer
	// are both blocked from renaming, but they are not the same thing.
	path := wsConfigFile(t)
	api := wsTestAPI()
	api.roleOf["ws-sem"] = "Contributor"
	h := &wsHarness{
		filterPicks: []string{"ws-sem"},
		numberPicks: []string{wsActionBack},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if !strings.Contains(out, "Contributor") {
		t.Errorf("panel must name the actual role:\n%s", out)
	}
}
