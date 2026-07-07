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
	return []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DP - DEV - SemMod\";initial catalog=HR;integrated security=ClaimsToken;semanticmodelid=12995bce-ace2-401b-a5fb-6b8dd6a45ead"}}}`)
}

func TestReportDatasetRefFlatByConnection(t *testing.T) {
	item := LocalItem{Type: "Report", Parts: []Part{
		{Path: "definition.pbir", Content: []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DP - DEV - SemMod\";initial catalog=HR;semanticmodelid=12995bce-ace2-401b-a5fb-6b8dd6a45ead"}}}`)},
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
	_, _, err := Execute(rf, "tok", w1, BuildPlan([]LocalItem{model}, nil), nil, modelsByWS, nil)
	if err != nil {
		t.Fatalf("execute w1: %v", err)
	}
	_, p, err := Execute(rf, "tok", w2, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{report}, nil), nil, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil), nil, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil), nil, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil), rb, modelsByWS, nil)
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
	_, pending, err := Execute(rf, "tok", target, BuildPlan([]LocalItem{model, report}, nil), rb, modelsByWS, nil)
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
