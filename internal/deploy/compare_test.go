package deploy

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestCompareClassifies(t *testing.T) {
	local := []LocalItem{
		{Type: "Notebook", DisplayName: "NB_New"},
		{Type: "Notebook", DisplayName: "NB_Exists"},
	}
	deployed := []fabric.Item{
		{ID: "id-exists", Type: "Notebook", DisplayName: "NB_Exists"},
		{ID: "id-orphan", Type: "Report", DisplayName: "R_Orphan"},
	}
	scope := map[string]bool{"Notebook": true, "Report": true}

	rows := Compare(local, deployed, scope)

	byName := map[string]CompareRow{}
	for _, r := range rows {
		byName[r.Name()] = r
	}
	if byName["NB_New"].Class != ClassNew {
		t.Errorf("NB_New should be New, got %v", byName["NB_New"].Class)
	}
	if byName["NB_Exists"].Class != ClassExists || byName["NB_Exists"].DeployedID != "id-exists" {
		t.Errorf("NB_Exists wrong: %+v", byName["NB_Exists"])
	}
	if byName["R_Orphan"].Class != ClassOrphan || byName["R_Orphan"].DeployedID != "id-orphan" {
		t.Errorf("R_Orphan should be Orphan: %+v", byName["R_Orphan"])
	}
}

func TestCompareIgnoresOutOfScopeOrphans(t *testing.T) {
	deployed := []fabric.Item{{ID: "x", Type: "Warehouse", DisplayName: "W"}}
	rows := Compare(nil, deployed, map[string]bool{"Notebook": true})
	for _, r := range rows {
		if r.Class == ClassOrphan {
			t.Errorf("out-of-scope type should not be flagged orphan: %+v", r)
		}
	}
}

// TestLogicalIDSeed pins which rows feed the logicalId -> GUID table shared by
// the preview and the publish: every local item already present in the target
// (whatever its content verdict) maps its logicalId to the deployed GUID; new
// items, orphans and items without a logicalId contribute nothing.
func TestLogicalIDSeed(t *testing.T) {
	rows := []CompareRow{
		{Class: ClassExists, Local: LocalItem{LogicalID: "l-exists"}, DeployedID: "g-exists"},
		{Class: ClassChanged, Local: LocalItem{LogicalID: "l-changed"}, DeployedID: "g-changed"},
		{Class: ClassUnchanged, Local: LocalItem{LogicalID: "l-same"}, DeployedID: "g-same"},
		{Class: ClassNew, Local: LocalItem{LogicalID: "l-new"}},
		{Class: ClassOrphan, Deployed: fabric.Item{ID: "g-orphan"}, DeployedID: "g-orphan"},
		{Class: ClassExists, Local: LocalItem{}, DeployedID: "g-nological"},
	}
	got := LogicalIDSeed(rows)
	want := map[string]string{"l-exists": "g-exists", "l-changed": "g-changed", "l-same": "g-same"}
	if len(got) != len(want) {
		t.Fatalf("seed = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("seed[%q] = %q, want %q", k, got[k], v)
		}
	}
}
