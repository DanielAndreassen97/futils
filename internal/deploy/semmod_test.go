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

func newSemmodSQLRebinder(t *testing.T) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DP - DEV - Config"},
			{ID: "test-config", DisplayName: "DP - TEST - Config"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-config":  {{ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
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
