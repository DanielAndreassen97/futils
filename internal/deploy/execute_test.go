package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// recordingFabric extends fakeFabric (from resolve_test.go) to capture
// create/update/rebind calls. created holds the def pointer as passed, so
// tests can assert a zero-part item was created with a nil definition.
type recordingFabric struct {
	fakeFabric
	created         []*fabric.Definition
	createdNames    []string
	createdPayloads []json.RawMessage            // creationPayload per CreateItem call
	sqlPolls        int                          // GetLakehouseSqlEndpoint call count
	sqlFailFirst    int                          // fail the first N endpoint polls
	updates         map[string]fabric.Definition // existingID -> def
	rebinds         [][3]string                  // {workspaceID, reportID, datasetID}
	rebindErr       error                        // when set, RebindReport returns it
	metaUpdates     []metaUpdate                 // UpdateItem (PATCH) calls
	deletes         []string                     // DeleteItem(id) calls
	updateItemErr   error                        // when set, UpdateItem returns it (description-sync failure)
	createErr       error                        // when set, CreateItem returns it (publish failure)
}

type metaUpdate struct{ id, displayName, description string }

func (r *recordingFabric) UpdateItem(token, ws, id, displayName, description string) error {
	r.metaUpdates = append(r.metaUpdates, metaUpdate{id, displayName, description})
	return r.updateItemErr
}

func (r *recordingFabric) CreateItem(token, ws, name, typ string, def *fabric.Definition, creationPayload json.RawMessage, folderID string) (fabric.Item, error) {
	if r.createErr != nil {
		return fabric.Item{}, r.createErr
	}
	r.created = append(r.created, def)
	r.createdNames = append(r.createdNames, name)
	r.createdPayloads = append(r.createdPayloads, creationPayload)
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
	r.rebinds = append(r.rebinds, [3]string{ws, reportID, datasetID})
	return r.rebindErr
}
func (r *recordingFabric) DeleteItem(token, ws, id string) error {
	r.deletes = append(r.deletes, id)
	return nil
}

func decodePart(t *testing.T, def *fabric.Definition, suffix string) string {
	t.Helper()
	if def == nil {
		t.Fatalf("definition is nil")
	}
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

func TestExecuteEncodesPartsAsBase64(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{
			Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid-nb",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("id=SOME-GUID")}},
		},
	}}

	res, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res) != 1 || res[0].Err != nil {
		t.Fatalf("result: %+v", res)
	}
	if got := decodePart(t, rf.created[0], "notebook-content.py"); got != "id=SOME-GUID" {
		t.Errorf("part content not preserved through encode/decode: %q", got)
	}
}

// Verified live 2026-07-17: the items API rejects "parts": null with 400
// "Parts: Must be a non-empty collection" — a zero-part item (Warehouse,
// SQLDatabase, a bare Lakehouse) must be created WITHOUT a definition field,
// and an update has no definition to push (only metadata is synced).
func TestExecuteZeroPartItemOmitsDefinition(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "Warehouse", DisplayName: "WH_Main", Description: "main"}},
		{Action: ActionUpdate, ExistingID: "wh-existing", Item: LocalItem{Type: "Warehouse", DisplayName: "WH_Other"}},
	}

	res, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("result %s: %v", r.Name, r.Err)
		}
	}
	if len(rf.created) != 1 || rf.created[0] != nil {
		t.Errorf("zero-part create must pass a nil definition, got %+v", rf.created)
	}
	if len(rf.updates) != 0 {
		t.Errorf("zero-part update must not call UpdateItemDefinition, got %v", rf.updates)
	}
	// Metadata (description) sync still runs for both, and the update result
	// keeps its existing ID so downstream passes can reference the item.
	if len(rf.metaUpdates) != 2 {
		t.Errorf("expected 2 metadata syncs, got %+v", rf.metaUpdates)
	}
	if res[1].ID != "wh-existing" {
		t.Errorf("update result ID = %q, want wh-existing", res[1].ID)
	}
}

// GetLakehouseSqlEndpoint on recordingFabric can be primed to fail the first
// N calls, simulating a lakehouse whose SQL endpoint is still provisioning.
func (r *recordingFabric) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	r.sqlPolls++
	if r.sqlPolls <= r.sqlFailFirst {
		return "", "", fmt.Errorf("lakehouse has no SQL endpoint yet (still provisioning?)")
	}
	return r.fakeFabric.GetLakehouseSqlEndpoint(token, ws, lhID)
}

// A create must pass .platform's creationPayload through (Warehouse collation,
// Lakehouse enableSchemas); items without one pass nil.
func TestExecutePassesCreationPayload(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	payload := json.RawMessage(`{"defaultCollation":"Latin1_General_100_CI_AS_KS_WS_SC_UTF8"}`)
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "Warehouse", DisplayName: "WH_A", CreationPayload: payload}},
		{Action: ActionCreate, Item: LocalItem{Type: "Notebook", DisplayName: "NB_A",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}}},
	}

	_, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(rf.createdPayloads[0]) != string(payload) {
		t.Errorf("creationPayload not passed through: %s", rf.createdPayloads[0])
	}
	if rf.createdPayloads[1] != nil {
		t.Errorf("item without creationPayload must pass nil, got %s", rf.createdPayloads[1])
	}
}

// After creating a Lakehouse, Execute must wait for its SQL analytics endpoint
// to provision (later items may resolve $sqlendpoint against it); a wait that
// never succeeds becomes a warning, not an error.
func TestExecuteWaitsForLakehouseSQLEndpoint(t *testing.T) {
	restoreInterval := sqlEndpointPollInterval
	restoreAttempts := sqlEndpointWaitAttempts
	sqlEndpointPollInterval = 0
	sqlEndpointWaitAttempts = 5
	t.Cleanup(func() {
		sqlEndpointPollInterval = restoreInterval
		sqlEndpointWaitAttempts = restoreAttempts
	})

	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	lakehouse := PlannedItem{Action: ActionCreate, Item: LocalItem{
		Type: "Lakehouse", DisplayName: "LH_A",
		Parts: []Part{{Path: "lakehouse.metadata.json", Content: []byte(`{"defaultSchema":"dbo"}`)}},
	}}

	// Provisions on the third poll → success, no warning.
	rf := &recordingFabric{sqlFailFirst: 2, fakeFabric: fakeFabric{itemsByWS: map[string][]fabric.Item{}}}
	res, _, err := Execute(rf, "tok", target, []PlannedItem{lakehouse}, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res[0].Err != nil || res[0].Warning != "" {
		t.Errorf("expected clean result after provisioning succeeded, got %+v", res[0])
	}
	if rf.sqlPolls != 3 {
		t.Errorf("expected 3 endpoint polls, got %d", rf.sqlPolls)
	}

	// Never provisions → published with a warning; updates must NOT wait.
	rf = &recordingFabric{sqlFailFirst: 1 << 30, fakeFabric: fakeFabric{itemsByWS: map[string][]fabric.Item{}}}
	update := lakehouse
	update.Action = ActionUpdate
	update.ExistingID = "lh-old"
	res, _, err = Execute(rf, "tok", target, []PlannedItem{lakehouse, update}, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res[0].Err != nil || !strings.Contains(res[0].Warning, "SQL endpoint") {
		t.Errorf("expected SQL endpoint warning on create, got %+v", res[0])
	}
	if res[1].Warning != "" {
		t.Errorf("update of existing lakehouse must not wait/warn, got %+v", res[1])
	}
	if rf.sqlPolls != sqlEndpointWaitAttempts {
		t.Errorf("expected %d polls (create only), got %d", sqlEndpointWaitAttempts, rf.sqlPolls)
	}
}

// The notebook definition API processes parts in payload order and requires
// the content file before any settings .json (fabric-cicd #869), and .ipynb
// content must be flagged with format=ipynb in the definition envelope.
func TestExecuteNotebookPartOrderAndIpynbFormat(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{
			Type: "Notebook", DisplayName: "NB_A",
			Parts: []Part{
				{Path: "extra-settings.json", Content: []byte("{}")},
				{Path: "environment.yml", Content: []byte("x: 1")},
				{Path: "notebook-content.ipynb", Content: []byte(`{"cells":[]}`)},
			},
		},
	}}

	_, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	def := rf.created[0]
	if def.Format != "ipynb" {
		t.Errorf("Format = %q, want ipynb", def.Format)
	}
	var order []string
	for _, p := range def.Parts {
		order = append(order, p.Path)
	}
	want := []string{"notebook-content.ipynb", "environment.yml", "extra-settings.json"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("part order = %v, want %v", order, want)
	}
}

// A .py-format notebook carries no format flag, and non-notebook items keep
// their discovery part order untouched.
func TestExecuteFormatAndOrderLeftAloneForOtherShapes(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{
			Type: "Notebook", DisplayName: "NB_Py",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}},
		}},
		{Action: ActionCreate, Item: LocalItem{
			Type: "Report", DisplayName: "R_A",
			Parts: []Part{
				{Path: "report.json", Content: []byte("{}")},
				{Path: "definition.pbir", Content: []byte("{}")},
			},
		}},
		{Action: ActionCreate, Item: LocalItem{
			Type: "SparkJobDefinition", DisplayName: "SJD_A",
			Parts: []Part{{Path: "SparkJobDefinitionV1.json", Content: []byte("{}")}},
		}},
	}

	_, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	byName := map[string]*fabric.Definition{}
	for i, n := range rf.createdNames {
		byName[n] = rf.created[i]
	}
	if f := byName["NB_Py"].Format; f != "" {
		t.Errorf("py notebook Format = %q, want empty", f)
	}
	if got := byName["R_A"].Parts[0].Path; got != "report.json" {
		t.Errorf("report part order changed, first = %q", got)
	}
	if f := byName["SJD_A"].Format; f != "SparkJobDefinitionV2" {
		t.Errorf("SparkJobDefinition Format = %q, want SparkJobDefinitionV2", f)
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
	res, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
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

func TestExecuteSetsDescriptionOnCreate(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Description: "My desc",
			Parts:       []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}}
	if _, _, err := Execute(rf, "tok", target, plan, nil, nil, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rf.metaUpdates) != 1 {
		t.Fatalf("want 1 metadata update, got %d: %+v", len(rf.metaUpdates), rf.metaUpdates)
	}
	got := rf.metaUpdates[0]
	if got.id != "NB_A-newid" || got.displayName != "NB_A" || got.description != "My desc" {
		t.Errorf("metadata update = %+v, want {NB_A-newid NB_A My desc}", got)
	}
}

func TestExecuteSetsDescriptionOnUpdate(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action: ActionUpdate, ExistingID: "existing-id",
		Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Description: "Updated desc",
			Parts:       []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}}
	if _, _, err := Execute(rf, "tok", target, plan, nil, nil, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rf.metaUpdates) != 1 || rf.metaUpdates[0].id != "existing-id" || rf.metaUpdates[0].description != "Updated desc" {
		t.Errorf("want metadata update of existing-id with 'Updated desc', got %+v", rf.metaUpdates)
	}
}

func TestExecuteDescriptionFailureIsNonFatal(t *testing.T) {
	rf := &recordingFabric{
		fakeFabric: fakeFabric{
			workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
			itemsByWS:  map[string][]fabric.Item{},
		},
		updateItemErr: fmt.Errorf("429 after retries"),
	}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", LogicalID: "lid",
			Description: "My desc",
			Parts:       []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}}
	results, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	// The definition published fine; only the description PATCH failed. That must
	// be a Warning (item still deployed), never an Err (which counts as failure).
	if results[0].Err != nil {
		t.Errorf("description failure must not set Err, got %v", results[0].Err)
	}
	if results[0].Warning == "" {
		t.Errorf("description failure should set a Warning, got empty")
	}
	if results[0].ID != "NB_A-newid" {
		t.Errorf("item should still be recorded as published, got ID %q", results[0].ID)
	}
}

// TestExecuteDoneCounterIncrements drives a multi-item plan (one of which fails
// to publish) and asserts the done counter lands at len(plan): it must advance
// once per item, on the failure path as well as success, so the spinner's
// "Publishing X/Y" can reach Y even when some items error.
func TestExecuteDoneCounterIncrements(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", LogicalID: "a",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}}},
		// No Parts → buildDefinition path still produces a definition, but give it a
		// part so it publishes cleanly; the counter must advance regardless.
		{Action: ActionCreate, Item: LocalItem{Type: "Notebook", DisplayName: "NB_B", LogicalID: "b",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("y=2")}}}},
		{Action: ActionCreate, Item: LocalItem{Type: "Notebook", DisplayName: "NB_C", LogicalID: "c",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("z=3")}}}},
	}

	var done int64
	res, _, err := Execute(rf, "tok", target, plan, nil, nil, &done)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res) != len(plan) {
		t.Fatalf("want %d results, got %d", len(plan), len(res))
	}
	if done != int64(len(plan)) {
		t.Errorf("done counter = %d, want %d (one per item)", done, len(plan))
	}
}

// TestExecuteDoneCounterAdvancesOnFailure confirms the counter advances even
// when an item's publish errors out (the early-return path inside the loop).
func TestExecuteDoneCounterAdvancesOnFailure(t *testing.T) {
	rf := &recordingFabric{
		fakeFabric: fakeFabric{
			workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
			itemsByWS:  map[string][]fabric.Item{},
		},
		createErr: fmt.Errorf("publish boom"),
	}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	// CreateItem errors, exercising the error-`return` path inside the per-item
	// closure — the counter must still advance.
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item: LocalItem{Type: "Notebook", DisplayName: "NB_X", LogicalID: "x",
			Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
	}}

	var done int64
	res, _, err := Execute(rf, "tok", target, plan, nil, nil, &done)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("expected one failed result, got %+v", res)
	}
	if done != 1 {
		t.Errorf("done counter = %d, want 1 (must advance on failure path too)", done)
	}
}

// byPathPBIR / byConnectionPBIR build the two PBIR dataset-reference shapes.
func byPathPBIR(modelName string) []byte {
	return []byte(`{"datasetReference":{"byPath":{"path":"../` + modelName + `.SemanticModel"}}}`)
}

func byConnectionPBIR() []byte {
	return []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DW - DEV - SemMod\";initial catalog=HR;integrated security=ClaimsToken;semanticmodelid=ffff1111-2222-3333-4444-555566667777"}}}`)
}

func TestReportDatasetRefFlatByConnection(t *testing.T) {
	item := LocalItem{Type: "Report", Parts: []Part{
		{Path: "definition.pbir", Content: []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DW - DEV - SemMod\";initial catalog=HR;semanticmodelid=ffff1111-2222-3333-4444-555566667777"}}}`)},
	}}
	if got := reportDatasetRef(item, nil, nil); got.Kind != refByConnection {
		t.Errorf("flat connectionString should classify as refByConnection, got %v", got.Kind)
	}
}

// runRebindPass is a helper that runs Execute over a plan accumulating into a
// shared modelsByWS map, then resolves the pending rebinds via RebindReports —
// mirroring how runDeploy threads the two phases.
func runRebindPass(t *testing.T, rf *recordingFabric, target fabric.Workspace, plan []PlannedItem, modelsByWS map[string]map[string]string) ([]Result, []ReportRebindOutcome) {
	t.Helper()
	res, pending, err := Execute(rf, "tok", target, plan, nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("item %s failed: %v", r.Name, r.Err)
		}
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
	return res, outcomes
}

func TestExecuteRebindReportToModelInSameRun(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}

	model := LocalItem{Type: "SemanticModel", DisplayName: "MyModel", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "MyReport", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MyModel")}}}

	plan := BuildPlan([]LocalItem{report, model}, nil, "") // ordered: model then report
	modelsByWS := map[string]map[string]string{}
	runRebindPass(t, rf, target, plan, modelsByWS)

	if len(rf.rebinds) != 1 {
		t.Fatalf("want 1 rebind, got %d", len(rf.rebinds))
	}
	if rf.rebinds[0][0] != "ws-test" {
		t.Errorf("rebind workspace = %q, want ws-test", rf.rebinds[0][0])
	}
	if rf.rebinds[0][2] != "MyModel-newid" {
		t.Errorf("rebind dataset = %q, want MyModel-newid", rf.rebinds[0][2])
	}
}

// findRebind reports whether a RebindReport(ws, report, dataset) call was made.
func findRebind(rf *recordingFabric, ws, reportID, datasetID string) bool {
	for _, rb := range rf.rebinds {
		if rb[0] == ws && rb[1] == reportID && rb[2] == datasetID {
			return true
		}
	}
	return false
}

// TestRebindReportsCrossGroupSameWorkspace proves fix #2: a model deployed in
// one group and a report in a SEPARATE group, both targeting the same
// workspace, still rebind correctly — regardless of which group deploys first,
// because modelsByWS is accumulated across every Execute call.
func TestRebindReportsCrossGroupSameWorkspace(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	model := LocalItem{Type: "SemanticModel", DisplayName: "MyModel", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "MyReport", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MyModel")}}}

	// modelGroupFirst controls the order the two single-item groups are executed.
	run := func(t *testing.T, modelGroupFirst bool) {
		rf := &recordingFabric{fakeFabric: fakeFabric{
			workspaces: []fabric.Workspace{target},
			itemsByWS:  map[string][]fabric.Item{},
		}}
		groups := [][]LocalItem{{report}, {model}}
		if modelGroupFirst {
			groups = [][]LocalItem{{model}, {report}}
		}
		modelsByWS := map[string]map[string]string{}
		var pending []PendingReportRebind
		for _, g := range groups {
			plan := BuildPlan(g, nil, "")
			_, p, err := Execute(rf, "tok", target, plan, nil, modelsByWS, nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			pending = append(pending, p...)
		}
		// No rebind happens inline during a per-group Execute anymore.
		if len(rf.rebinds) != 0 {
			t.Fatalf("rebind must not happen inline during Execute, got %d", len(rf.rebinds))
		}
		outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
		for _, o := range outcomes {
			if o.Err != nil {
				t.Fatalf("rebind outcome error: %v", o.Err)
			}
		}
		if !findRebind(rf, "ws-test", "MyReport-newid", "MyModel-newid") {
			t.Errorf("report not rebound to its model GUID; rebinds=%v", rf.rebinds)
		}
	}

	t.Run("report group first", func(t *testing.T) { run(t, false) })
	t.Run("model group first", func(t *testing.T) { run(t, true) })
}

// TestRebindReportsWorkspaceIsolation proves a model named "X" in workspace W1
// must NOT bind a report that references "X" but lives in workspace W2.
// The report is silently skipped (no Warning, no RebindReport call) because the
// model is absent from modelsByWS for W2 — the same silent-skip rule as an
// incremental deploy where the model was Unchanged.
func TestRebindReportsWorkspaceIsolation(t *testing.T) {
	w1 := fabric.Workspace{ID: "ws-1", DisplayName: "W1"}
	w2 := fabric.Workspace{ID: "ws-2", DisplayName: "W2"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{w1, w2},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	model := LocalItem{Type: "SemanticModel", DisplayName: "X", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "Rep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("X")}}}

	modelsByWS := map[string]map[string]string{}
	var pending []PendingReportRebind
	// Model deploys to W1; report deploys to W2.
	_, _, err := Execute(rf, "tok", w1, BuildPlan([]LocalItem{model}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute w1: %v", err)
	}
	_, p, err := Execute(rf, "tok", w2, BuildPlan([]LocalItem{report}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute w2: %v", err)
	}
	pending = append(pending, p...)

	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
	if len(rf.rebinds) != 0 {
		t.Fatalf("model in W1 must not bind a report in W2, got rebinds=%v", rf.rebinds)
	}
	// Silent skip: model is not in W2's modelsByWS entry, so no outcome is produced.
	if len(outcomes) != 0 {
		t.Fatalf("cross-workspace model-missing must produce no outcome (silent skip), got %+v", outcomes)
	}
}

// TestRebindReportsByConnectionNoWarning proves that a byConnection report with an
// active rebinder (rebinderActive=true) and a model not published this run produces
// no warning and no rebind call — the in-payload rewrite (RebindReportConnection)
// handled the binding, so the post-deploy pass is a clean no-op.
func TestRebindReportsByConnectionNoWarning(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{target},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	report := LocalItem{Type: "Report", DisplayName: "ConnRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byConnectionPBIR()}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// rebinderActive=true: the in-payload rewrite ran, model not published this run
	// → clean no-op (no rebind call, no warning).
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)

	if len(rf.rebinds) != 0 {
		t.Fatalf("byConnection report must NOT trigger a RebindReport, got %v", rf.rebinds)
	}
	for _, r := range outcomes {
		if r.Warning != "" {
			t.Errorf("byConnection report with active rebinder must not warn, got %q", r.Warning)
		}
	}
}

// TestRebindReportsModelMissingSkipsSilently: a byPath report whose model is
// NOT in modelsByWS (e.g. only the report was in the deploy selection, the
// SemanticModel was Unchanged and never published this run) must be silently
// skipped — no Warning outcome, no RebindReport call. The report is already
// correctly bound in the target workspace; emitting a warning every time is a
// false alarm.
func TestRebindReportsModelMissingSkipsSilently(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{target},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	report := LocalItem{Type: "Report", DisplayName: "Orphan", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MissingModel")}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)

	if len(rf.rebinds) != 0 {
		t.Fatalf("missing model must not rebind, got %v", rf.rebinds)
	}
	// Silent skip: no outcome at all when model is absent from modelsByWS.
	if len(outcomes) != 0 {
		t.Fatalf("byPath model-missing must produce no outcome (silent skip), got %+v", outcomes)
	}
}

// TestRebindReportsErrorSetsErr: a RebindReport API failure must surface as an
// Err outcome (so runDeploy folds it into Result.Err), not a warning.
func TestRebindReportsErrorSetsErr(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{
		fakeFabric: fakeFabric{
			workspaces: []fabric.Workspace{target},
			itemsByWS:  map[string][]fabric.Item{},
		},
		rebindErr: fmt.Errorf("403 forbidden"),
	}
	model := LocalItem{Type: "SemanticModel", DisplayName: "MyModel", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "MyReport", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MyModel")}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("rebind failure must produce an Err outcome, got %+v", outcomes)
	}
}

// TestReportDatasetRefKinds documents the three-way parse of definition.pbir.
func TestReportDatasetRefKinds(t *testing.T) {
	byPath := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MyModel")}}}
	if ref := reportDatasetRef(byPath, nil, nil); ref.Kind != refByPath || ref.ModelName != "MyModel" {
		t.Errorf("byPath = %+v, want {refByPath MyModel}", ref)
	}
	byConn := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: byConnectionPBIR()}}}
	if ref := reportDatasetRef(byConn, nil, nil); ref.Kind != refByConnection {
		t.Errorf("byConnection = %+v, want refByConnection", ref)
	}
	none := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: []byte(`{}`)}}}
	if ref := reportDatasetRef(none, nil, nil); ref.Kind != refNone {
		t.Errorf("empty pbir = %+v, want refNone", ref)
	}
	noPbir := LocalItem{Type: "Report", Parts: []Part{{Path: "report.json", Content: []byte(`{}`)}}}
	if ref := reportDatasetRef(noPbir, nil, nil); ref.Kind != refNone {
		t.Errorf("no pbir = %+v, want refNone", ref)
	}
}

// TestRebindReportsByConnectionCoDeployedBinds proves that a byConnection report
// whose semantic model was CREATED in the same run (not in the pre-deploy target
// index, so the payload kept the baseline GUID) gets rebound via modelsByWS
// once that map is populated post-deploy.
func TestRebindReportsByConnectionCoDeployedBinds(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{}}
	pending := []PendingReportRebind{{
		WorkspaceID: "ws-1", ReportID: "rep-1", ReportName: "R",
		Ref: datasetRef{Kind: refByConnection, ModelName: "HR"},
	}}
	models := map[string]map[string]string{"ws-1": {"HR": "new-hr-guid"}}
	outcomes := RebindReports(rf, "tok", models, pending, true)
	if len(rf.rebinds) != 1 || rf.rebinds[0][2] != "new-hr-guid" {
		t.Fatalf("co-deployed byConnection report must rebind to the same-run model, got rebinds=%v", rf.rebinds)
	}
	if len(outcomes) != 0 {
		t.Errorf("clean rebind should produce no outcome, got %+v", outcomes)
	}
}

// TestRebindReportsByConnectionCoDeployedBindsEndToEnd proves the full chain:
// Execute parses a flat-connectionString byConnection report via reportDatasetRef
// (recovering ModelName="HR" from "initial catalog"), publishes a same-run "HR"
// SemanticModel into modelsByWS, and the post-deploy RebindReports binds the
// report to that same-run model GUID. This exercises reportDatasetRef →
// ModelName → co-deployed-bind that the hand-built pending test skips.
func TestRebindReportsByConnectionCoDeployedBindsEndToEnd(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{target},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	model := LocalItem{Type: "SemanticModel", DisplayName: "HR", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "ConnRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byConnectionPBIR()}}} // initial catalog=HR

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil, ""), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(pending) != 1 || pending[0].Ref.Kind != refByConnection || pending[0].Ref.ModelName != "HR" {
		t.Fatalf("reportDatasetRef must recover ModelName=HR for a flat byConnection report, got %+v", pending)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
	if !findRebind(rf, "ws-test", "ConnRep-newid", "HR-newid") {
		t.Fatalf("co-deployed byConnection report must rebind to the same-run HR GUID, got rebinds=%v", rf.rebinds)
	}
	for _, o := range outcomes {
		if o.Err != nil || o.Warning != "" {
			t.Errorf("clean co-deployed rebind must produce no outcome, got %+v", o)
		}
	}
}

// structuredByConnectionPBIR builds the fabric-cicd structured byConnection
// shape: no connectionString, the model referenced only by its baseline GUID.
func structuredByConnectionPBIR(modelGUID string) []byte {
	return []byte(`{"datasetReference":{"byConnection":{"connectionString":null,"pbiServiceModelId":null,"pbiModelVirtualServerName":"sobe_wowvirtualserver","pbiModelDatabaseName":"` + modelGUID + `","name":"EntityDataSource","connectionType":"pbiServiceLive"}}}`)
}

// TestRebindReportsStructuredByConnectionCoDeployedBinds proves that a report in
// the STRUCTURED byConnection shape (no connectionString to recover a name from)
// still binds to its co-deployed model: the pending ref must resolve the model
// name from the baseline GUID via the rebinder's baseline index.
func TestRebindReportsStructuredByConnectionCoDeployedBinds(t *testing.T) {
	dev := fabric.Workspace{ID: "ws-dev", DisplayName: "DEV"}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{dev, target},
		itemsByWS: map[string][]fabric.Item{
			"ws-dev":  {{ID: devHRModel, DisplayName: "HR", Type: "SemanticModel"}},
			"ws-test": {}, // model absent pre-deploy: it is CREATED this run
		},
	}}
	rb, err := NewRebinder(rf, "tok", []fabric.Workspace{dev}, []fabric.Workspace{target}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}

	model := LocalItem{Type: "SemanticModel", DisplayName: "HR", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "StructRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: structuredByConnectionPBIR(devHRModel)}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil, ""), rb, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending, true)
	if !findRebind(rf, "ws-test", "StructRep-newid", "HR-newid") {
		t.Fatalf("structured byConnection report must rebind to the co-deployed HR GUID, got rebinds=%v (pending=%+v)", rf.rebinds, pending)
	}
	for _, o := range outcomes {
		if o.Err != nil {
			t.Errorf("clean rebind must not error, got %+v", o)
		}
	}
}

// TestRebindReportsByConnectionStaleCatalogPrefersBaseline pins the authoritative
// precedence for byConnection co-deploy binding: the baseline-index name for the
// reference's semanticmodelid GUID wins over a STALE "initial catalog" name in the
// connectionString. A report whose cached connectionString still names the model
// "HR_old" but whose GUID resolves (via the baseline index) to "HR" must bind to
// the co-deployed "HR" model, never a same-run "HR_old" — mirroring
// RebindReportConnection, which treats the GUID as authoritative so a stale catalog
// string can't misbind. (Regression: the old AltNames path tried the flat name FIRST.)
func TestRebindReportsByConnectionStaleCatalogPrefersBaseline(t *testing.T) {
	dev := fabric.Workspace{ID: "ws-dev", DisplayName: "DEV"}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{dev, target},
		itemsByWS: map[string][]fabric.Item{
			"ws-dev":  {{ID: devHRModel, DisplayName: "HR", Type: "SemanticModel"}},
			"ws-test": {}, // both models created this run
		},
	}}
	rb, err := NewRebinder(rf, "tok", []fabric.Workspace{dev}, []fabric.Workspace{target}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}

	// Stale catalog name "HR_old", but the semanticmodelid GUID maps to "HR".
	stalePBIR := []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DW - DEV - SemMod\";initial catalog=HR_old;integrated security=ClaimsToken;semanticmodelid=` + devHRModel + `"}}}`)
	hr := LocalItem{Type: "SemanticModel", DisplayName: "HR", LogicalID: "lid-hr",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	hrOld := LocalItem{Type: "SemanticModel", DisplayName: "HR_old", LogicalID: "lid-old",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table Y")}}}
	report := LocalItem{Type: "Report", DisplayName: "StaleRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: stalePBIR}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{hr, hrOld, report}, nil, ""), rb, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	RebindReports(rf, "tok", modelsByWS, pending, true)

	if !findRebind(rf, "ws-test", "StaleRep-newid", "HR-newid") {
		t.Fatalf("stale-catalog byConnection report must bind to the baseline-resolved HR model, got rebinds=%v", rf.rebinds)
	}
	if findRebind(rf, "ws-test", "StaleRep-newid", "HR_old-newid") {
		t.Fatal("report bound to the STALE catalog name HR_old — baseline GUID resolution must win")
	}
}

// TestExecutePendingRebindUsesSubstitutedPBIR proves the pending ref is parsed
// from the SUBSTITUTED pbir, not the raw part: a custom substitution rewrites
// the model name to the target form, and the co-deployed model (published under
// that target name) must still be matched.
func TestExecutePendingRebindUsesSubstitutedPBIR(t *testing.T) {
	dev := fabric.Workspace{ID: "ws-dev", DisplayName: "DEV"}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{dev, target},
		itemsByWS:  map[string][]fabric.Item{"ws-dev": {}, "ws-test": {}},
	}}
	rb, err := NewRebinder(rf, "tok", []fabric.Workspace{dev}, []fabric.Workspace{target}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	rb.SetSubstitutions([]Substitution{{FindValue: "SM_Sales [dev]", Literal: "SM_Sales"}})

	// Baseline GUID unknown to the baseline index — the name candidate from the
	// (substituted) connectionString is the only way to match the model.
	pbir := []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DEV\";initial catalog=SM_Sales [dev];integrated security=ClaimsToken;semanticmodelid=99999999-9999-9999-9999-999999999999"}}}`)
	model := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", LogicalID: "lid-m",
		Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("table X")}}}
	report := LocalItem{Type: "Report", DisplayName: "SalesRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: pbir}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil, ""), rb, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	RebindReports(rf, "tok", modelsByWS, pending, true)
	if !findRebind(rf, "ws-test", "SalesRep-newid", "SM_Sales-newid") {
		t.Fatalf("pending ref must be parsed from the substituted pbir (SM_Sales), got rebinds=%v (pending=%+v)", rf.rebinds, pending)
	}
}

// TestRebindReportsByConnectionNoRebinderWarns proves that when no rebinder is
// configured (rebinderActive=false) and the model was not deployed this run,
// a byConnection report emits a warning — the regression where the warning was
// silently dropped.
func TestRebindReportsByConnectionNoRebinderWarns(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{}}
	pending := []PendingReportRebind{{
		WorkspaceID: "ws-1", ReportID: "rep-1", ReportName: "R",
		Ref: datasetRef{Kind: refByConnection, ModelName: "HR"},
	}}
	outcomes := RebindReports(rf, "tok", map[string]map[string]string{}, pending, false)
	if len(rf.rebinds) != 0 {
		t.Fatalf("no model to bind to — must not call RebindReport, got %v", rf.rebinds)
	}
	if len(outcomes) != 1 || !strings.Contains(outcomes[0].Warning, "byConnection") {
		t.Fatalf("no rebinder → byConnection report must warn, got %+v", outcomes)
	}
}

func TestDeleteItems(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	targets := []DeleteTarget{
		{ID: "id-1", Name: "NB_Gone", Type: "Notebook"},
		{ID: "id-2", Name: "PL_Gone", Type: "DataPipeline"},
	}
	results := DeleteItems(rf, "tok", target, targets)

	if len(rf.deletes) != 2 || rf.deletes[0] != "id-1" || rf.deletes[1] != "id-2" {
		t.Fatalf("want both ids deleted in order, got %v", rf.deletes)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Action != ActionDelete || results[0].Name != "NB_Gone" || results[0].Err != nil {
		t.Errorf("result 0 = %+v, want {NB_Gone Notebook Delete nil-err}", results[0])
	}
	if ActionDelete.String() != "Delete" {
		t.Errorf("ActionDelete.String() = %q, want Delete", ActionDelete.String())
	}
}

// New items land in the workspace folder derived from their repo path: the
// folder tree is created top-down (parent before child) and each create gets
// the right folderId; existing items are never placed. A re-run reuses folders
// already present.
func TestExecuteCreatesAndUsesWorkspaceFolders(t *testing.T) {
	// A recordingFabric variant that tracks folders + the folderId per create.
	rf := &folderRecordingFabric{recordingFabric: recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws", DisplayName: "T"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}}
	target := fabric.Workspace{ID: "ws", DisplayName: "T"}
	plan := []PlannedItem{
		{Action: ActionCreate, WorkspaceFolder: "Notebooks/Config",
			Item: LocalItem{Type: "Notebook", DisplayName: "NB_A", Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}}},
		{Action: ActionCreate, WorkspaceFolder: "Notebooks/Config",
			Item: LocalItem{Type: "Notebook", DisplayName: "NB_B", Parts: []Part{{Path: "notebook-content.py", Content: []byte("y=1")}}}},
		{Action: ActionCreate, WorkspaceFolder: "", // root
			Item: LocalItem{Type: "Notebook", DisplayName: "NB_root", Parts: []Part{{Path: "notebook-content.py", Content: []byte("z=1")}}}},
	}

	res, _, err := Execute(rf, "tok", target, plan, nil, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Name, r.Err)
		}
	}
	// Two folders created (Notebooks, then Notebooks/Config), parent first.
	if len(rf.createdFolderPaths) != 2 || rf.createdFolderPaths[0] != "Notebooks" || rf.createdFolderPaths[1] != "Config" {
		t.Errorf("folder create order = %v, want [Notebooks Config]", rf.createdFolderPaths)
	}
	// Both NBs in Config share the same folderId; the root item gets "".
	configID := rf.itemFolder["NB_A"]
	if configID == "" || rf.itemFolder["NB_B"] != configID {
		t.Errorf("NB_A/NB_B should share the Config folderId, got %q / %q", configID, rf.itemFolder["NB_B"])
	}
	if rf.itemFolder["NB_root"] != "" {
		t.Errorf("root item must get empty folderId, got %q", rf.itemFolder["NB_root"])
	}
}

type folderRecordingFabric struct {
	recordingFabric
	folders            []fabric.Folder
	createdFolderPaths []string          // DisplayName of each folder created, in order
	itemFolder         map[string]string // item name -> folderId passed to CreateItem
}

func (f *folderRecordingFabric) ListFolders(token, ws string) ([]fabric.Folder, error) {
	return f.folders, nil
}
func (f *folderRecordingFabric) CreateFolder(token, ws, name, parentID string) (fabric.Folder, error) {
	fld := fabric.Folder{ID: "id-" + name, DisplayName: name, ParentFolderID: parentID}
	f.folders = append(f.folders, fld)
	f.createdFolderPaths = append(f.createdFolderPaths, name)
	return fld, nil
}
func (f *folderRecordingFabric) CreateItem(token, ws, name, typ string, def *fabric.Definition, cp json.RawMessage, folderID string) (fabric.Item, error) {
	if f.itemFolder == nil {
		f.itemFolder = map[string]string{}
	}
	f.itemFolder[name] = folderID
	return f.recordingFabric.CreateItem(token, ws, name, typ, def, cp, folderID)
}

// End-to-end for the same-run shortcut chain: a lakehouse whose OneLake
// shortcut points at another lakehouse CREATED IN THE SAME RUN must (a) publish
// after its target (shortcut-dependency ordering) and (b) have the shortcut
// rebound to the target's fresh GUID (RegisterTargetItem) — not silently keep
// the baseline GUID and read the baseline environment's data.
func TestExecuteShortcutIntoSameRunCreatedLakehouse(t *testing.T) {
	devBronze := "0b0b0b0b-1111-2222-3333-444455556666"
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "ws-dev", DisplayName: "DEV"},
			{ID: "ws-test", DisplayName: "TEST"},
		},
		itemsByWS: map[string][]fabric.Item{
			"ws-dev":  {{ID: devBronze, DisplayName: "LH_Bronze", Type: "Lakehouse"}},
			"ws-test": {}, // empty target: both lakehouses are created this run
		},
	}}
	rb, err := NewRebinder(rf, "tok",
		[]fabric.Workspace{{ID: "ws-dev", DisplayName: "DEV"}},
		[]fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	shortcut := []byte(`[{"name":"bronze_data","target":{"type":"OneLake","oneLake":{"workspaceId":"ws-dev","itemId":"` + devBronze + `","path":"Tables/t"}}}]`)
	// Deliberately wrong order: the consumer is planned before its target.
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_Silver",
			Parts: []Part{{Path: "shortcuts.metadata.json", Content: shortcut}}}},
		{Action: ActionCreate, Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_Bronze",
			Parts: []Part{{Path: "lakehouse.metadata.json", Content: []byte(`{}`)}}}},
	}

	res, _, err := Execute(rf, "tok", fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}, plan, rb, nil, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("result %s: %v", r.Name, r.Err)
		}
	}
	if len(rf.createdNames) != 2 || rf.createdNames[0] != "LH_Bronze" || rf.createdNames[1] != "LH_Silver" {
		t.Fatalf("shortcut target must be created first, got %v", rf.createdNames)
	}
	published := decodePart(t, rf.created[1], "shortcuts.metadata.json")
	if !strings.Contains(published, "LH_Bronze-newid") || strings.Contains(published, devBronze) {
		t.Errorf("shortcut must rebind to the same-run-created target GUID:\n%s", published)
	}
	if !strings.Contains(published, "ws-test") || strings.Contains(published, "ws-dev") {
		t.Errorf("shortcut workspace must rebind to the target workspace:\n%s", published)
	}
}

// An update whose local folder has no definition parts (non-shell type) skips
// the definition call — that must surface as a warning, not read as a clean
// green Update that "deployed" the deletion of the item's parts.
func TestExecuteNilDefinitionUpdateWarns(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	plan := []PlannedItem{{
		Action: ActionUpdate, ExistingID: "lh-1",
		Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_Bare"},
	}}
	res, _, err := Execute(rf, "tok", fabric.Workspace{ID: "ws-test"}, plan, nil, nil, nil)
	if err != nil || len(res) != 1 || res[0].Err != nil {
		t.Fatalf("execute: %v / %+v", err, res)
	}
	if len(rf.updates) != 0 {
		t.Errorf("no definition call expected, got %v", rf.updates)
	}
	if !strings.Contains(res[0].Warning, "definition") {
		t.Errorf("nil-definition update must warn, got %q", res[0].Warning)
	}
}

// A shell-only update with no git content stays silent — the skip is expected,
// not noteworthy.
func TestExecuteShellOnlyUpdateStaysSilent(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	plan := []PlannedItem{{
		Action: ActionUpdate, ExistingID: "wh-1",
		Item: LocalItem{Type: "Warehouse", DisplayName: "WH"},
	}}
	res, _, err := Execute(rf, "tok", fabric.Workspace{ID: "ws-test"}, plan, nil, nil, nil)
	if err != nil || len(res) != 1 {
		t.Fatalf("execute: %v / %+v", err, res)
	}
	if res[0].Warning != "" {
		t.Errorf("shell-only zero-content update must not warn, got %q", res[0].Warning)
	}
}

// Definition files discovery dropped for a shell-only type must ride the
// result as a warning — a .sql edit in a Warehouse folder is otherwise
// invisible in the deploy output.
func TestExecuteShellPartsWarning(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	plan := []PlannedItem{{
		Action: ActionCreate,
		Item:   LocalItem{Type: "Warehouse", DisplayName: "WH", ShellParts: 3},
	}}
	res, _, err := Execute(rf, "tok", fabric.Workspace{ID: "ws-test"}, plan, nil, nil, nil)
	if err != nil || len(res) != 1 || res[0].Err != nil {
		t.Fatalf("execute: %v / %+v", err, res)
	}
	if !strings.Contains(res[0].Warning, "3 schema file(s)") {
		t.Errorf("shell-skipped git content must warn, got %q", res[0].Warning)
	}
}

// A byPath report co-deployed with its model must publish with the model's
// FRESH GUID in a byConnection reference: the model deploys first (publish
// order), gets registered in the target index, and the report's byPath
// converts against it — the items API rejects byPath outright.
func TestExecuteByPathReportBindsToSameRunModel(t *testing.T) {
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "ws-dev", DisplayName: "DEV"},
			{ID: "ws-test", DisplayName: "TEST"},
		},
		itemsByWS: map[string][]fabric.Item{"ws-dev": {}, "ws-test": {}},
	}}
	rb, err := NewRebinder(rf, "tok",
		[]fabric.Workspace{{ID: "ws-dev", DisplayName: "DEV"}},
		[]fabric.Workspace{{ID: "ws-test", DisplayName: "TEST"}}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "SemanticModel", DisplayName: "HR",
			Parts: []Part{{Path: "definition/model.tmdl", Content: []byte("model HR")}}}},
		{Action: ActionCreate, Item: LocalItem{Type: "Report", DisplayName: "HR Dashboard",
			Parts: []Part{{Path: "definition.pbir", Content: []byte(`{"datasetReference":{"byPath":{"path":"../HR.SemanticModel"}}}`)}}}},
	}

	res, _, err := Execute(rf, "tok", fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}, plan, rb, map[string]map[string]string{}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("result %s: %v", r.Name, r.Err)
		}
	}
	published := decodePart(t, rf.created[1], "definition.pbir")
	if !strings.Contains(published, `"pbiModelDatabaseName": "HR-newid"`) {
		t.Errorf("report must bind to the same-run model's fresh GUID:\n%s", published)
	}
	if strings.Contains(published, "byPath") {
		t.Errorf("published pbir must not retain byPath:\n%s", published)
	}
}
