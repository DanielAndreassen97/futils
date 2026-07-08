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
