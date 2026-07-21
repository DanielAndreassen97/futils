package deploy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// DEV-baseline GUIDs baked into the git notebook.
const (
	devConfigLH = "11111111-1111-1111-1111-111111111111"
	devConfigWS = "22222222-2222-2222-2222-222222222222"
	devSilverLH = "33333333-3333-3333-3333-333333333333"
	devHRModel  = "ffff1111-2222-3333-4444-555566667777"
)

func rebindNotebook(defaultLH, defaultWS, defaultName, knownID string) []byte {
	return []byte(`# Fabric notebook source

# METADATA ********************

# META {
# META   "dependencies": {
# META     "lakehouse": {
# META       "default_lakehouse": "` + defaultLH + `",
# META       "default_lakehouse_name": "` + defaultName + `",
# META       "default_lakehouse_workspace_id": "` + defaultWS + `",
# META       "known_lakehouses": [
# META         { "id": "` + knownID + `" }
# META       ]
# META     }
# META   }
# META }
`)
}

// newRebindFixture wires a fake with both envs. Baseline (DEV) holds the GUIDs
// committed in git; target (TEST) holds the same names under new GUIDs.
func newRebindFixture(t *testing.T, overrides map[string]Override) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DW - DEV - Config"},
			{ID: "dev-data", DisplayName: "DW - DEV - Data"},
			{ID: "dev-semmod", DisplayName: "DW - DEV - SemMod"},
			{ID: "test-config", DisplayName: "DW - TEST - Config"},
			{ID: "test-data", DisplayName: "DW - TEST - Data"},
			{ID: "test-semmod", DisplayName: "DW - TEST - SemMod"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-config":  {{ID: devConfigLH, DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"dev-data":    {{ID: devSilverLH, DisplayName: "LH_Silver", Type: "Lakehouse"}},
			"dev-semmod":  {{ID: devHRModel, DisplayName: "HR", Type: "SemanticModel"}},
			"test-config": {{ID: "test-config-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"test-data":   {{ID: "test-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}},
			"test-semmod": {{ID: "test-hr-model", DisplayName: "HR", Type: "SemanticModel"}},
		},
	}
	baselineWS := []fabric.Workspace{f.workspaces[0], f.workspaces[1], f.workspaces[2]}
	targetWS := []fabric.Workspace{f.workspaces[3], f.workspaces[4], f.workspaces[5]}
	rb, err := NewRebinder(f, "tok", baselineWS, targetWS, overrides)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

func TestRebindDefaultLakehouseByName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, outcome := rb.RebindNotebookLakehouses(in)
	s := string(out)
	if !strings.Contains(s, "test-config-lh") {
		t.Errorf("default_lakehouse not rebound to target GUID:\n%s", s)
	}
	if !strings.Contains(s, "\"test-config\"") || strings.Contains(s, devConfigWS) {
		t.Errorf("default_lakehouse_workspace_id not rebound to target workspace:\n%s", s)
	}
	if strings.Contains(s, devConfigLH) {
		t.Errorf("baseline default_lakehouse GUID still present:\n%s", s)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("expected no unresolved refs, got %#v", outcome.Unresolved)
	}
}

func TestRebindKnownLakehouseViaBaselineName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, _ := rb.RebindNotebookLakehouses(in)
	s := string(out)
	if !strings.Contains(s, "test-silver-lh") {
		t.Errorf("known_lakehouse LH_Silver not rebound:\n%s", s)
	}
	if strings.Contains(s, devSilverLH) {
		t.Errorf("baseline known_lakehouse GUID still present:\n%s", s)
	}
}

func TestRebindUnresolvedKnownLakehouse(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// A known lakehouse GUID that exists in NEITHER env -> unresolved, untouched.
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if !strings.Contains(string(out), unknown) {
		t.Error("unresolved GUID should be left unchanged in content")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].GUID != unknown || outcome.Unresolved[0].Location != "known_lakehouses" {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}

func TestRebindOverrideTakesPrecedence(t *testing.T) {
	// Override maps the unknown baseline GUID directly to LH_Silver by name; add
	// LH_Silver to the target so the override resolves.
	overrides := map[string]Override{
		"99999999-9999-9999-9999-999999999999": {ItemType: "Lakehouse", ItemName: "LH_Silver"},
	}
	rb := newRebindFixture(t, overrides)
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("override should resolve the GUID, got unresolved %#v", outcome.Unresolved)
	}
	if !strings.Contains(string(out), "test-silver-lh") || strings.Contains(string(out), unknown) {
		t.Errorf("override not applied:\n%s", string(out))
	}
}

func TestRebindOverrideOnDefaultLakehouse(t *testing.T) {
	// Override the DEV default-lakehouse GUID directly to a different target
	// lakehouse by name — proves the override beats the stored
	// default_lakehouse_name zero-config path.
	overrides := map[string]Override{
		devConfigLH: {ItemType: "Lakehouse", ItemName: "LH_Silver"},
	}
	rb := newRebindFixture(t, overrides)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("expected no unresolved, got %#v", outcome.Unresolved)
	}
	s := string(out)
	if !strings.Contains(s, "test-silver-lh") {
		t.Errorf("override on default_lakehouse not applied (expected test-silver-lh):\n%s", s)
	}
	if strings.Contains(s, devConfigLH) {
		t.Errorf("baseline default_lakehouse GUID still present:\n%s", s)
	}
}

func TestUnresolvedCarriesReason(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// A known lakehouse GUID that exists in NEITHER env → name-unknown.
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Reason != ReasonNameUnknown {
		t.Fatalf("unresolved = %#v (want one with ReasonNameUnknown)", outcome.Unresolved)
	}
}

func TestUnresolvedReasonNotInTarget(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// default_lakehouse_name that the baseline has but the target lacks: use a
	// name absent from the target env. Build a notebook whose default name is
	// "LH_Ghost" (not in target) — name path → not-in-target.
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_Ghost", "")
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Reason != ReasonNotInTarget {
		t.Fatalf("unresolved = %#v (want one with ReasonNotInTarget)", outcome.Unresolved)
	}
}

func TestRebindNonNotebookUnchanged(t *testing.T) {
	rb := newRebindFixture(t, nil)
	plain := []byte("table Foo\ncolumn Bar\n")
	out, outcome := rb.RebindNotebookLakehouses(plain)
	if string(out) != string(plain) || len(outcome.Unresolved) != 0 {
		t.Errorf("non-notebook content should pass through unchanged")
	}
}

func TestRebindNotebookReportsChanges(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", outcome.Unresolved)
	}
	// Expect three changes: default lakehouse (Lakehouse), its workspace (Workspace),
	// and the known lakehouse (Lakehouse). Deduped by Old.
	kinds := map[string]int{}
	for _, c := range outcome.Changes {
		kinds[c.Kind]++
		if c.Old == "" || c.New == "" || c.Old == c.New {
			t.Errorf("bad change %#v", c)
		}
	}
	if kinds["Lakehouse"] != 2 || kinds["Workspace"] != 1 {
		t.Fatalf("change kinds = %#v (want Lakehouse:2 Workspace:1)", kinds)
	}
	// The default-lakehouse change must map the DEV GUID to the TEST GUID.
	var found bool
	for _, c := range outcome.Changes {
		if c.Old == devConfigLH && c.New == "test-config-lh" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing default_lakehouse change %s→test-config-lh in %#v", devConfigLH, outcome.Changes)
	}
}

func TestRebindNotebookChangesCarryNames(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	_, outcome := rb.RebindNotebookLakehouses(in)

	var lhNamed, wsNamed bool
	for _, c := range outcome.Changes {
		if c.Kind == "Lakehouse" && c.Old == devConfigLH && c.Name == "LH_ConfigLog" {
			lhNamed = true
		}
		if c.Kind == "Workspace" && c.Name == "DW - TEST - Config" {
			wsNamed = true
		}
	}
	if !lhNamed {
		t.Errorf("Lakehouse change missing Name %q: %#v", "LH_ConfigLog", outcome.Changes)
	}
	if !wsNamed {
		t.Errorf("Workspace change missing Name %q: %#v", "DW - TEST - Config", outcome.Changes)
	}
}

func TestDefaultLakehouseNoNameUnknownGUIDReason(t *testing.T) {
	rb := newRebindFixture(t, nil)
	unknown := "88888888-8888-8888-8888-888888888888"
	// No default_lakehouse_name, GUID not in baseline → name-unknown, not not-in-target.
	in := rebindNotebook(unknown, devConfigWS, "", devSilverLH)
	_, outcome := rb.RebindNotebookLakehouses(in)
	var dl *UnresolvedRef
	for i := range outcome.Unresolved {
		if outcome.Unresolved[i].Location == "default_lakehouse" {
			dl = &outcome.Unresolved[i]
		}
	}
	if dl == nil {
		t.Fatalf("expected a default_lakehouse unresolved, got %#v", outcome.Unresolved)
	}
	if dl.Reason != ReasonNameUnknown {
		t.Errorf("default_lakehouse Reason = %q, want %q", dl.Reason, ReasonNameUnknown)
	}
}

// flatPBIR builds a byConnection report definition in the Power BI Desktop
// flat-connection-string shape (the real on-disk form).
func flatPBIR(ws, catalog, guid string) []byte {
	return []byte(`{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/report/definitionProperties/2.0.0/schema.json",
  "version": "4.0",
  "datasetReference": {
    "byConnection": {
      "connectionString": "Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/` + ws + `\";initial catalog=` + catalog + `;integrated security=ClaimsToken;semanticmodelid=` + guid + `"
    }
  }
}`)
}

// structuredPBIR builds the fabric-cicd structured byConnection shape.
func structuredPBIR(guid string) []byte {
	return []byte(`{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/report/definitionProperties/1.0.0/schema.json",
  "version": "4.0",
  "datasetReference": {
    "byConnection": {
      "connectionString": null,
      "pbiModelDatabaseName": "` + guid + `",
      "pbiModelVirtualServerName": "sobe_wowvirtualserver",
      "name": "EntityDataSource",
      "connectionType": "pbiServiceXmlaStyleLive"
    }
  }
}`)
}

// pbirModelID parses a rewritten pbir and returns its byConnection
// pbiModelDatabaseName (the bound model GUID).
func pbirModelID(t *testing.T, content []byte) string {
	t.Helper()
	var p struct {
		DatasetReference struct {
			ByConnection struct {
				PbiModelDatabaseName string  `json:"pbiModelDatabaseName"`
				ConnectionString     *string `json:"connectionString"`
			} `json:"byConnection"`
		} `json:"datasetReference"`
	}
	if err := json.Unmarshal(content, &p); err != nil {
		t.Fatalf("rewritten pbir not valid JSON: %v\n%s", err, content)
	}
	if p.DatasetReference.ByConnection.ConnectionString != nil {
		t.Errorf("canonical form must null out connectionString, got %q", *p.DatasetReference.ByConnection.ConnectionString)
	}
	return p.DatasetReference.ByConnection.PbiModelDatabaseName
}

// pbirSchema parses a rewritten pbir and returns its $schema field.
func pbirSchema(t *testing.T, content []byte) string {
	t.Helper()
	var p struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(content, &p); err != nil {
		t.Fatalf("rewritten pbir not valid JSON: %v\n%s", err, content)
	}
	return p.Schema
}

func TestRebindReportConnectionFlatResolves(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "Daniel - Testing"}
	out, outcome := rb.RebindReportConnection(item, flatPBIR("DW - DEV - SemMod", "HR", devHRModel))

	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Errorf("bound model GUID = %q, want test-hr-model", got)
	}
	if len(outcome.ReportBindings) != 1 {
		t.Fatalf("want 1 ReportBinding, got %d", len(outcome.ReportBindings))
	}
	b := outcome.ReportBindings[0]
	if b.Report != "Daniel - Testing" || b.Model != "HR" || b.Workspace != "DW - TEST - SemMod" {
		t.Errorf("ReportBinding = %+v", b)
	}
	// Report rebinds must not appear in the generic rebind summary.
	if len(outcome.Changes) != 0 {
		t.Errorf("report rebind must not emit RebindChange, got %+v", outcome.Changes)
	}
	// The rewrite pins the canonical 1.0.0 definitionProperties schema.
	if s := pbirSchema(t, out); !strings.HasSuffix(s, "definitionProperties/1.0.0/schema.json") {
		t.Errorf("$schema = %q, want suffix definitionProperties/1.0.0/schema.json", s)
	}
}

func TestRebindReportConnectionStructuredResolves(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "R"}
	out, outcome := rb.RebindReportConnection(item, structuredPBIR(devHRModel))
	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Errorf("bound model GUID = %q, want test-hr-model", got)
	}
	if len(outcome.ReportBindings) != 1 || outcome.ReportBindings[0].Model != "HR" {
		t.Errorf("ReportBindings = %+v", outcome.ReportBindings)
	}
	// Report rebinds must not appear in the generic rebind summary.
	if len(outcome.Changes) != 0 {
		t.Errorf("report rebind must not emit RebindChange, got %+v", outcome.Changes)
	}
}

func TestRebindReportConnectionOverrideWins(t *testing.T) {
	// Baseline GUID points at a model whose name isn't "HR"; an override maps it.
	rb := newRebindFixture(t, map[string]Override{
		"99999999-9999-9999-9999-999999999999": {ItemType: "SemanticModel", ItemName: "HR"},
	})
	item := LocalItem{Type: "Report", DisplayName: "R"}
	out, _ := rb.RebindReportConnection(item, flatPBIR("DW - DEV - SemMod", "Unknown", "99999999-9999-9999-9999-999999999999"))
	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Errorf("override should resolve to HR target GUID, got %q", got)
	}
}

func TestRebindReportConnectionUnresolved(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "R"}
	// GUID not in baseline, catalog name not in target → not-in-target unresolved.
	unknownGUID := "77777777-0000-0000-0000-000000000000"
	in := flatPBIR("DW - DEV - SemMod", "NoSuchModel", unknownGUID)
	out, outcome := rb.RebindReportConnection(item, in)
	if string(out) != string(in) {
		t.Errorf("unresolved binding must leave content unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].ItemType != "SemanticModel" {
		t.Fatalf("want 1 SemanticModel UnresolvedRef, got %+v", outcome.Unresolved)
	}
	if len(outcome.ReportBindings) != 0 {
		t.Errorf("unresolved binding must not produce a ReportBinding")
	}
}

func TestRebindReportConnectionNameUnknown(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "R"}
	// Structured form whose GUID is in NEITHER the baseline index nor an override:
	// no name to match by → name-unknown, content untouched.
	unknown := "aaaaaaaa-0000-0000-0000-000000000000"
	in := structuredPBIR(unknown)
	out, outcome := rb.RebindReportConnection(item, in)
	if string(out) != string(in) {
		t.Errorf("name-unknown binding must leave content unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].ItemType != "SemanticModel" || outcome.Unresolved[0].Reason != ReasonNameUnknown {
		t.Fatalf("want 1 SemanticModel UnresolvedRef with ReasonNameUnknown, got %+v", outcome.Unresolved)
	}
	if len(outcome.ReportBindings) != 0 {
		t.Errorf("name-unknown binding must not produce a ReportBinding")
	}
}

// A byPath reference MUST convert to byConnection at publish — the items API
// rejects byPath outright ("Fabric REST API only supports byConnection
// references", live 400 on report import). The path's folder name resolves as
// the model name in the target env.
func TestRebindReportConnectionByPathConverts(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := []byte(`{"datasetReference":{"byPath":{"path":"../HR.SemanticModel"}}}`)
	out, outcome := rb.RebindReportConnection(LocalItem{Type: "Report", DisplayName: "R"}, in)
	s := string(out)
	if !strings.Contains(s, `"pbiModelDatabaseName": "test-hr-model"`) {
		t.Errorf("byPath must convert to canonical byConnection against the target model:\n%s", s)
	}
	if strings.Contains(s, "byPath") {
		t.Errorf("converted pbir must not retain the byPath reference:\n%s", s)
	}
	if len(outcome.ReportBindings) != 1 || outcome.ReportBindings[0].Model != "HR" {
		t.Fatalf("want one HR ReportBinding, got %+v", outcome.ReportBindings)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("resolvable byPath must not surface unresolved refs: %+v", outcome.Unresolved)
	}
}

// A byPath reference whose model exists in NO target workspace can't convert —
// it stays as-is and surfaces as unresolved, so the user sees the real cause
// instead of the API's import error.
func TestRebindReportConnectionByPathUnresolved(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := []byte(`{"datasetReference":{"byPath":{"path":"../Drift.SemanticModel"}}}`)
	out, outcome := rb.RebindReportConnection(LocalItem{Type: "Report", DisplayName: "R"}, in)
	if string(out) != string(in) {
		t.Errorf("unresolvable byPath must leave content unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].ItemType != "SemanticModel" || outcome.Unresolved[0].Reason != ReasonNotInTarget {
		t.Fatalf("want 1 SemanticModel UnresolvedRef with ReasonNotInTarget, got %+v", outcome.Unresolved)
	}
	if len(outcome.ReportBindings) != 0 {
		t.Errorf("unresolvable byPath must not produce a ReportBinding")
	}
}

func TestRebindPartDispatchesReport(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "Daniel - Testing"}
	out, outcome := rb.RebindPart(item, "definition.pbir", flatPBIR("DW - DEV - SemMod", "HR", devHRModel))
	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Errorf("RebindPart did not rebind report binding, model GUID = %q", got)
	}
	if len(outcome.ReportBindings) != 1 {
		t.Errorf("RebindPart should surface the ReportBinding, got %d", len(outcome.ReportBindings))
	}
}

func TestRebindPartIgnoresNonPbirReportPart(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "R"}
	in := []byte(`{"some":"report.json content"}`)
	out, outcome := rb.RebindPart(item, "report.json", in)
	if string(out) != string(in) || len(outcome.ReportBindings) != 0 {
		t.Errorf("non-pbir report part must pass through unchanged")
	}
}

func TestRebindReportConnectionStaleCatalogUsesGUID(t *testing.T) {
	// Connection string carries a STALE catalog name but the correct baseline GUID.
	rb := newRebindFixture(t, nil)
	item := LocalItem{Type: "Report", DisplayName: "R"}
	out, outcome := rb.RebindReportConnection(item, flatPBIR("DW - DEV - SemMod", "OldNameBeforeRename", devHRModel))
	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Errorf("should resolve via semanticmodelid baseline GUID despite stale catalog, got %q", got)
	}
	if len(outcome.ReportBindings) != 1 || outcome.ReportBindings[0].Model != "HR" {
		t.Errorf("binding should resolve to HR via the GUID, got %+v", outcome.ReportBindings)
	}
}

func TestRebindReportConnectionNoRebindChange(t *testing.T) {
	// Report rebinds must NOT appear in the generic rebind summary (out.Changes);
	// only in out.ReportBindings.
	rb := newRebindFixture(t, nil)
	out, outcome := rb.RebindReportConnection(LocalItem{Type: "Report", DisplayName: "R"},
		flatPBIR("DW - DEV - SemMod", "HR", devHRModel))
	if got := pbirModelID(t, out); got != "test-hr-model" {
		t.Fatalf("expected rebind to target, got %q", got)
	}
	if len(outcome.Changes) != 0 {
		t.Errorf("report rebind must not emit RebindChange (shown only in Report bindings), got %+v", outcome.Changes)
	}
	if len(outcome.ReportBindings) != 1 {
		t.Errorf("expected one ReportBinding, got %d", len(outcome.ReportBindings))
	}
}

func TestRebindReportConnectionCatalogGUIDNoBaselineMiss(t *testing.T) {
	// Flat shape, no semanticmodelid, catalog is a GUID NOT in the baseline index.
	rb := newRebindFixture(t, nil)
	in := []byte(`{"datasetReference":{"byConnection":{"connectionString":"Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DW - DEV - SemMod\";initial catalog=aaaaaaaa-0000-0000-0000-000000000000"}}}`)
	out, outcome := rb.RebindReportConnection(LocalItem{Type: "Report", DisplayName: "R"}, in)
	if string(out) != string(in) {
		t.Errorf("unresolvable catalog-GUID must leave content unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Reason != ReasonNameUnknown {
		t.Fatalf("catalog-GUID miss must be ReasonNameUnknown (never pass the GUID as a name), got %+v", outcome.Unresolved)
	}
}

// A lakehouse's shortcuts.metadata.json OneLake targets carry baseline
// workspace+item GUIDs that must rebind to the target env by name, exactly
// like a notebook's lakehouse block. External targets and self-references
// (empty/zero GUIDs) are left untouched.
func TestRebindShortcutsOneLakeTargets(t *testing.T) {
	rb := newRebindFixture(t, nil)
	shortcuts := []byte(`[
	  {"name": "silver_orders", "path": "Tables",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "` + devConfigWS + `-data-placeholder",
	     "itemId": "` + devSilverLH + `",
	     "path": "Tables/orders"}}},
	  {"name": "self_ref", "path": "Files",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "00000000-0000-0000-0000-000000000000",
	     "itemId": "00000000-0000-0000-0000-000000000000",
	     "path": "Files/local"}}},
	  {"name": "external", "path": "Files",
	   "target": {"type": "AmazonS3", "amazonS3": {
	     "location": "https://bucket.s3.amazonaws.com", "subpath": "/data"}}}
	]`)
	item := LocalItem{Type: "Lakehouse", DisplayName: "LH_Bronze"}
	out, outcome := rb.RebindPart(item, "shortcuts.metadata.json", shortcuts)
	s := string(out)

	if !strings.Contains(s, "test-silver-lh") || strings.Contains(s, devSilverLH) {
		t.Errorf("OneLake itemId not rebound baseline→target:\n%s", s)
	}
	if !strings.Contains(s, "test-data") {
		t.Errorf("OneLake workspaceId not rebound to target workspace:\n%s", s)
	}
	if !strings.Contains(s, "00000000-0000-0000-0000-000000000000") {
		t.Errorf("self-reference (zero GUID) must be left untouched:\n%s", s)
	}
	if !strings.Contains(s, "bucket.s3.amazonaws.com") {
		t.Errorf("external target must be left untouched:\n%s", s)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("expected clean resolve, got %#v", outcome.Unresolved)
	}
}

// A OneLake shortcut whose target item resolves in neither env is left
// unchanged and surfaced as unresolved.
func TestRebindShortcutsUnresolved(t *testing.T) {
	rb := newRebindFixture(t, nil)
	unknown := "99999999-9999-9999-9999-999999999999"
	shortcuts := []byte(`[{"name": "x", "path": "Tables",
	  "target": {"type": "OneLake", "oneLake": {
	    "workspaceId": "` + devConfigWS + `", "itemId": "` + unknown + `", "path": "Tables/x"}}}]`)
	out, outcome := rb.RebindPart(LocalItem{Type: "Lakehouse", DisplayName: "LH"}, "shortcuts.metadata.json", shortcuts)
	if !strings.Contains(string(out), unknown) {
		t.Error("unresolved shortcut target should be left unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Location != "shortcut target" {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}

// A zero-GUID workspaceId on a REAL shortcut target must not leak into other
// shortcuts: the rewrite is scoped per shortcut, so the resolved item's
// workspace is written on that shortcut alone while a neighbouring
// self-reference keeps its zero GUIDs untouched.
func TestRebindShortcutsZeroWorkspaceScopedRewrite(t *testing.T) {
	rb := newRebindFixture(t, nil)
	zero := "00000000-0000-0000-0000-000000000000"
	shortcuts := []byte(`[
	  {"name": "silver", "path": "Tables",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "` + zero + `", "itemId": "` + devSilverLH + `", "path": "Tables/t"}}},
	  {"name": "self", "path": "Files",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "` + zero + `", "itemId": "` + zero + `", "path": "Files/f"}}}
	]`)
	out, outcome := rb.RebindPart(LocalItem{Type: "Lakehouse", DisplayName: "LH"}, "shortcuts.metadata.json", shortcuts)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", outcome.Unresolved)
	}
	got := parseShortcutTargets(t, out)
	if got["silver"] != [2]string{"test-data", "test-silver-lh"} {
		t.Errorf("real target must get the resolved item's workspace+item, got %v", got["silver"])
	}
	if got["self"] != [2]string{zero, zero} {
		t.Errorf("self-reference zero GUIDs must survive untouched, got %v", got["self"])
	}
}

// Two shortcuts sharing one baseline workspace GUID whose items resolve to
// DIFFERENT target workspaces must each get their own workspace — the old
// whole-file string replace with old-value dedup rewrote both to the first
// mapping, deploying a dead shortcut (workspaceId and itemId disagreeing).
func TestRebindShortcutsSharedBaselineWorkspaceDistinctTargets(t *testing.T) {
	rb := newRebindFixture(t, nil)
	sharedWS := "aaaa1111-1111-1111-1111-111111111111"
	shortcuts := []byte(`[
	  {"name": "config", "path": "Tables",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "` + sharedWS + `", "itemId": "` + devConfigLH + `", "path": "Tables/c"}}},
	  {"name": "silver", "path": "Tables",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "` + sharedWS + `", "itemId": "` + devSilverLH + `", "path": "Tables/s"}}}
	]`)
	out, outcome := rb.RebindPart(LocalItem{Type: "Lakehouse", DisplayName: "LH"}, "shortcuts.metadata.json", shortcuts)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", outcome.Unresolved)
	}
	got := parseShortcutTargets(t, out)
	if got["config"] != [2]string{"test-config", "test-config-lh"} {
		t.Errorf("config shortcut = %v, want test-config/test-config-lh", got["config"])
	}
	if got["silver"] != [2]string{"test-data", "test-silver-lh"} {
		t.Errorf("silver shortcut = %v, want test-data/test-silver-lh", got["silver"])
	}
}

// A shortcuts file the rebind has nothing to change must round-trip
// byte-identically — no re-marshal, no formatting churn.
func TestRebindShortcutsUntouchedFileKeepsBytes(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := []byte(`[
	  {"name": "self", "path": "Files",
	   "target": {"type": "OneLake", "oneLake": {
	     "workspaceId": "00000000-0000-0000-0000-000000000000",
	     "itemId": "00000000-0000-0000-0000-000000000000", "path": "Files/f"}}},
	  {"name": "ext", "path": "Files",
	   "target": {"type": "AmazonS3", "amazonS3": {"location": "https://b.s3.amazonaws.com", "subpath": "/d"}}}
	]`)
	out, _ := rb.RebindPart(LocalItem{Type: "Lakehouse", DisplayName: "LH"}, "shortcuts.metadata.json", in)
	if string(out) != string(in) {
		t.Errorf("no-change file must stay byte-identical:\n%s", out)
	}
}

// parseShortcutTargets maps shortcut name -> {workspaceId, itemId} from a
// rewritten shortcuts.metadata.json.
func parseShortcutTargets(t *testing.T, content []byte) map[string][2]string {
	t.Helper()
	var shortcuts []struct {
		Name   string `json:"name"`
		Target struct {
			OneLake *struct {
				WorkspaceID string `json:"workspaceId"`
				ItemID      string `json:"itemId"`
			} `json:"oneLake"`
		} `json:"target"`
	}
	if err := json.Unmarshal(content, &shortcuts); err != nil {
		t.Fatalf("parse rewritten shortcuts: %v\n%s", err, content)
	}
	got := map[string][2]string{}
	for _, sc := range shortcuts {
		if sc.Target.OneLake != nil {
			got[sc.Name] = [2]string{sc.Target.OneLake.WorkspaceID, sc.Target.OneLake.ItemID}
		}
	}
	return got
}
