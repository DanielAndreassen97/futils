package deploy

import (
	"strings"
	"testing"
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
