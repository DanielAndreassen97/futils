package deploy

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// recordingFabric extends fakeFabric (from resolve_test.go) to capture
// create/update/rebind calls.
type recordingFabric struct {
	fakeFabric
	created       []fabric.Definition
	createdNames  []string
	updates       map[string]fabric.Definition // existingID -> def
	rebinds       [][3]string                  // {workspaceID, reportID, datasetID}
	rebindErr     error                        // when set, RebindReport returns it
	metaUpdates   []metaUpdate                 // UpdateItem (PATCH) calls
	deletes       []string                     // DeleteItem(id) calls
	updateItemErr error                        // when set, UpdateItem returns it (description-sync failure)
	createErr     error                        // when set, CreateItem returns it (publish failure)
}

type metaUpdate struct{ id, displayName, description string }

func (r *recordingFabric) UpdateItem(token, ws, id, displayName, description string) error {
	r.metaUpdates = append(r.metaUpdates, metaUpdate{id, displayName, description})
	return r.updateItemErr
}

func (r *recordingFabric) CreateItem(token, ws, name, typ string, def *fabric.Definition) (fabric.Item, error) {
	if r.createErr != nil {
		return fabric.Item{}, r.createErr
	}
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
	r.rebinds = append(r.rebinds, [3]string{ws, reportID, datasetID})
	return r.rebindErr
}
func (r *recordingFabric) DeleteItem(token, ws, id string) error {
	r.deletes = append(r.deletes, id)
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
	return []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=...","pbiServiceModelId":null,"pbiModelVirtualServerName":"sobe_wowvirtualserver","pbiModelDatabaseName":"abc-123","name":"EntityDataSource","connectionType":"pbiServiceXmlaStyleLive"}}}`)
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
	outcomes := RebindReports(rf, "tok", modelsByWS, pending)
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

	plan := BuildPlan([]LocalItem{report, model}, nil) // ordered: model then report
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
			plan := BuildPlan(g, nil)
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
		outcomes := RebindReports(rf, "tok", modelsByWS, pending)
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
	_, _, err := Execute(rf, "tok", w1, BuildPlan([]LocalItem{model}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute w1: %v", err)
	}
	_, p, err := Execute(rf, "tok", w2, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute w2: %v", err)
	}
	pending = append(pending, p...)

	outcomes := RebindReports(rf, "tok", modelsByWS, pending)
	if len(rf.rebinds) != 0 {
		t.Fatalf("model in W1 must not bind a report in W2, got rebinds=%v", rf.rebinds)
	}
	// The report should carry a "not found in target workspace" warning, not be silent.
	if len(outcomes) != 1 || outcomes[0].Warning == "" {
		t.Fatalf("want 1 outcome with a warning, got %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Warning, "not found in target workspace") {
		t.Errorf("warning should explain the model is missing in the target ws, got %q", outcomes[0].Warning)
	}
}

// TestRebindReportsByConnectionWarns proves fix #3: a report using a
// byConnection dataset reference is NOT silently skipped — it gets a Warning.
func TestRebindReportsByConnectionWarns(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{target},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	report := LocalItem{Type: "Report", DisplayName: "ConnRep", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byConnectionPBIR()}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending)

	if len(rf.rebinds) != 0 {
		t.Fatalf("byConnection report must NOT trigger a RebindReport, got %v", rf.rebinds)
	}
	if len(outcomes) != 1 || outcomes[0].Warning == "" {
		t.Fatalf("byConnection report must produce a warning outcome, got %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Warning, "byConnection") {
		t.Errorf("warning should name the byConnection reference, got %q", outcomes[0].Warning)
	}
	if outcomes[0].ReportID != "ConnRep-newid" {
		t.Errorf("outcome must carry the report's deployed GUID, got %q", outcomes[0].ReportID)
	}
}

// TestRebindReportsModelMissingWarns: byPath reference whose model name is not
// in the target workspace's map → Warning, no rebind (e.g. the model wasn't
// part of this deploy selection).
func TestRebindReportsModelMissingWarns(t *testing.T) {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{target},
		itemsByWS:  map[string][]fabric.Item{},
	}}
	report := LocalItem{Type: "Report", DisplayName: "Orphan", LogicalID: "lid-r",
		Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MissingModel")}}}

	modelsByWS := map[string]map[string]string{}
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending)

	if len(rf.rebinds) != 0 {
		t.Fatalf("missing model must not rebind, got %v", rf.rebinds)
	}
	if len(outcomes) != 1 || outcomes[0].Warning == "" {
		t.Fatalf("missing model must warn, got %+v", outcomes)
	}
	if !strings.Contains(outcomes[0].Warning, `"MissingModel"`) {
		t.Errorf("warning should name the missing model, got %q", outcomes[0].Warning)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	outcomes := RebindReports(rf, "tok", modelsByWS, pending)
	if len(outcomes) != 1 || outcomes[0].Err == nil {
		t.Fatalf("rebind failure must produce an Err outcome, got %+v", outcomes)
	}
}

// TestReportDatasetRefKinds documents the three-way parse of definition.pbir.
func TestReportDatasetRefKinds(t *testing.T) {
	byPath := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: byPathPBIR("MyModel")}}}
	if ref := reportDatasetRef(byPath); ref.Kind != refByPath || ref.ModelName != "MyModel" {
		t.Errorf("byPath = %+v, want {refByPath MyModel}", ref)
	}
	byConn := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: byConnectionPBIR()}}}
	if ref := reportDatasetRef(byConn); ref.Kind != refByConnection {
		t.Errorf("byConnection = %+v, want refByConnection", ref)
	}
	none := LocalItem{Type: "Report", Parts: []Part{{Path: "definition.pbir", Content: []byte(`{}`)}}}
	if ref := reportDatasetRef(none); ref.Kind != refNone {
		t.Errorf("empty pbir = %+v, want refNone", ref)
	}
	noPbir := LocalItem{Type: "Report", Parts: []Part{{Path: "report.json", Content: []byte(`{}`)}}}
	if ref := reportDatasetRef(noPbir); ref.Kind != refNone {
		t.Errorf("no pbir = %+v, want refNone", ref)
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
