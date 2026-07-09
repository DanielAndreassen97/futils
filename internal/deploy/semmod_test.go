package deploy

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// semmodOneLake builds a Direct Lake on OneLake expression referencing a
// workspace GUID + lakehouse GUID, the on-disk form confirmed in the design doc.
func semmodOneLake(wsGUID, lhGUID string) []byte {
	return []byte(`expression DatabaseQuery =
		let
			Source = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/` + wsGUID + `/` + lhGUID + `")
		in
			Source
`)
}

func TestRebindOneLakeSource(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// devSilverLH lives in dev-data (workspace "dev-data"); target LH_Silver is
	// test-silver-lh in test-data.
	in := string(semmodOneLake("dev-data", devSilverLH))
	var out RebindOutcome
	got := rb.rebindOneLakeSources(in, &out)
	if !strings.Contains(got, "test-silver-lh") {
		t.Errorf("lakehouse GUID not rebound:\n%s", got)
	}
	if !strings.Contains(got, "test-data") || strings.Contains(got, devSilverLH) {
		t.Errorf("workspace GUID not rebound:\n%s", got)
	}
	// One Lakehouse change + one Workspace change.
	var lh, ws int
	for _, c := range out.Changes {
		switch c.Kind {
		case "Lakehouse":
			lh++
		case "Workspace":
			ws++
		}
	}
	if lh != 1 || ws != 1 {
		t.Fatalf("changes = %#v (want Lakehouse:1 Workspace:1)", out.Changes)
	}
}

// TestRebindOneLakeSharedWorkspaceDifferentTargets proves that two OneLake
// sources sharing the SAME baseline workspace whose lakehouses resolve to
// DIFFERENT target workspaces are each rewritten to their own workspace — the
// old-value dedup must not funnel both URLs into the first target workspace.
func TestRebindOneLakeSharedWorkspaceDifferentTargets(t *testing.T) {
	const (
		lhOne = "44444444-4444-4444-4444-444444444444"
		lhTwo = "55555555-5555-5555-5555-555555555555"
		tgtA  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tgtB  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-a", DisplayName: "TGT A"},
			{ID: "tgt-b", DisplayName: "TGT B"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-data": {
				{ID: lhOne, DisplayName: "LH_One", Type: "Lakehouse"},
				{ID: lhTwo, DisplayName: "LH_Two", Type: "Lakehouse"},
			},
			"tgt-a": {{ID: tgtA, DisplayName: "LH_One", Type: "Lakehouse"}},
			"tgt-b": {{ID: tgtB, DisplayName: "LH_Two", Type: "Lakehouse"}},
		},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1], f.workspaces[2]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let
	A = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/dev-data/` + lhOne + `"),
	B = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/dev-data/` + lhTwo + `")
in
	B`
	got, outcome := rb.RebindSemanticModel([]byte(in))
	s := string(got)
	if !strings.Contains(s, "onelake.dfs.fabric.microsoft.com/tgt-a/"+tgtA) {
		t.Errorf("LH_One must land in tgt-a:\n%s", s)
	}
	if !strings.Contains(s, "onelake.dfs.fabric.microsoft.com/tgt-b/"+tgtB) {
		t.Errorf("LH_Two must land in tgt-b (not funneled into tgt-a):\n%s", s)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("both lakehouses resolve — no unresolved expected, got %+v", outcome.Unresolved)
	}
}

func newSemmodSQLRebinder(t *testing.T) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DP - DEV - Config"},
			{ID: "test-config", DisplayName: "DP - TEST - Config"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-config":  {{ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}, {ID: "dev-ep-id", DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}},
			"test-config": {{ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"dev-lh":  {"devhost.datawarehouse.fabric.microsoft.com", "dev-ep-id"},
			"test-lh": {"testhost.datawarehouse.fabric.microsoft.com", "test-ep-id"},
		},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1]},
		nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

func semmodSQL(host, epID string) string {
	return `expression DatabaseQuery =
		let
			Source = Sql.Database("` + host + `", "` + epID + `")
		in
			Source
`
}

func TestRebindSQLSource(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	in := semmodSQL("devhost.datawarehouse.fabric.microsoft.com", "dev-ep-id")
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, "testhost.datawarehouse.fabric.microsoft.com") || !strings.Contains(got, "test-ep-id") {
		t.Errorf("SQL endpoint not rebound:\n%s", got)
	}
	if strings.Contains(got, "devhost") || strings.Contains(got, "dev-ep-id") {
		t.Errorf("baseline endpoint still present:\n%s", got)
	}
	if len(out.Changes) != 2 { // host + id
		t.Fatalf("changes = %#v (want host + id)", out.Changes)
	}
	for _, c := range out.Changes {
		if c.Kind != "SQL endpoint" {
			t.Errorf("unexpected change kind %q", c.Kind)
		}
	}
}

func TestRebindSQLSourceUnresolved(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	in := semmodSQL("unknownhost.datawarehouse.fabric.microsoft.com", "unknown-ep-id")
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, "unknown-ep-id") {
		t.Error("unresolved endpoint should be left unchanged")
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].ItemType != "SQL endpoint" {
		t.Fatalf("unresolved = %#v", out.Unresolved)
	}
}

func TestRebindSemanticModelSQLVariant(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	// SQL variant only (this fixture has no data workspace for OneLake).
	in := []byte(semmodSQL("devhost.datawarehouse.fabric.microsoft.com", "dev-ep-id"))
	out, outcome := rb.RebindSemanticModel(in)
	if strings.Contains(string(out), "devhost") {
		t.Errorf("SQL endpoint not rebound:\n%s", out)
	}
	if len(outcome.Changes) != 2 {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestRebindPartDispatchesSemanticModel(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Config"}
	content := []byte(semmodSQL("devhost.datawarehouse.fabric.microsoft.com", "dev-ep-id"))
	out, outcome := rb.RebindPart(item, "definition/expressions.tmdl", content)
	if strings.Contains(string(out), "devhost") || len(outcome.Changes) != 2 {
		t.Errorf("RebindPart did not rebind a SemanticModel part:\n%s\n%#v", out, outcome.Changes)
	}
	// A non-semmod, non-notebook part is untouched.
	plain := []byte(`Sql.Database("devhost.datawarehouse.fabric.microsoft.com", "dev-ep-id")`)
	out2, outcome2 := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "P"}, "pipeline-content.json", plain)
	if string(out2) != string(plain) || len(outcome2.Changes) != 0 {
		t.Error("non-semmod/non-notebook part should be untouched")
	}
}

// The baked GUID is a SQLEndpoint item (indexed by name), not a Lakehouse — the
// old baseEndpoints probe missed it; ItemByGUID must resolve it by name.
func TestRebindSQLViaNameIndex(t *testing.T) {
	dev := fabric.Workspace{ID: "ws-dev", DisplayName: "DEV"}
	tgt := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{dev, tgt},
		itemsByWS: map[string][]fabric.Item{
			"ws-dev":  {{ID: "dev-ep", DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}, {ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"ws-test": {{ID: "test-ep", DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}, {ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
		// GetLakehouseSqlEndpoint returns (host,id) per lakehouse id (host from the parent lakehouse).
		sqlByLH: map[string][2]string{"test-lh": {"test-host", "test-ep"}},
	}}
	rb, err := NewRebinder(rf, "tok", []fabric.Workspace{dev}, []fabric.Workspace{tgt}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `database = Sql.Database("dev-host", "dev-ep")`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if len(out.Unresolved) != 0 {
		t.Fatalf("must resolve via NameIndex, got unresolved %+v", out.Unresolved)
	}
	if !strings.Contains(got, `"test-host"`) || !strings.Contains(got, `"test-ep"`) {
		t.Fatalf("rewrite = %q, want test-host/test-ep", got)
	}
}

func TestItemsOfType(t *testing.T) {
	f := newIndexFixture() // from nameindex_test.go: ws-config has LH_ConfigLog, ws-data has LH_Silver+LH_Gold
	idx, _ := BuildNameIndex(f, "tok", f.workspaces)
	lakes := idx.ItemsOfType("Lakehouse")
	if len(lakes) != 3 {
		t.Fatalf("expected 3 lakehouses, got %d: %#v", len(lakes), lakes)
	}
	if len(idx.ItemsOfType("Notebook")) != 0 {
		t.Error("expected no notebooks")
	}
}
