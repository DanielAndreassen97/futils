package deploy

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func newIndexFixture() *fakeFabric {
	return &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "ws-config", DisplayName: "DP - TEST - Config"},
			{ID: "ws-data", DisplayName: "DP - TEST - Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"ws-config": {{ID: "lh-config", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"ws-data": {
				{ID: "lh-silver", DisplayName: "LH_Silver", Type: "Lakehouse"},
				{ID: "lh-gold", DisplayName: "LH_Gold", Type: "Lakehouse"},
			},
		},
	}
}

func TestBuildNameIndexForwardAndReverse(t *testing.T) {
	f := newIndexFixture()
	idx, err := BuildNameIndex(f, "tok", f.workspaces)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Reverse: GUID -> name+type.
	it, ok := idx.ItemByGUID("lh-silver")
	if !ok || it.Name != "LH_Silver" || it.Type != "Lakehouse" {
		t.Fatalf("ItemByGUID(lh-silver) = %#v ok=%v", it, ok)
	}
	// Forward: name+type -> GUID, carrying the workspace it lives in.
	it, ok = idx.ItemByName("LH_ConfigLog", "Lakehouse")
	if !ok || it.GUID != "lh-config" || it.WorkspaceID != "ws-config" {
		t.Fatalf("ItemByName(LH_ConfigLog) = %#v ok=%v", it, ok)
	}
}

func TestNameIndexMissesReturnFalse(t *testing.T) {
	f := newIndexFixture()
	idx, _ := BuildNameIndex(f, "tok", f.workspaces)
	if _, ok := idx.ItemByGUID("nope"); ok {
		t.Error("expected unknown GUID to miss")
	}
	if _, ok := idx.ItemByName("LH_ConfigLog", "Notebook"); ok {
		t.Error("expected wrong type to miss")
	}
}

func TestLookupNameStatus(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{{ID: "w1", DisplayName: "W1"}, {ID: "w2", DisplayName: "W2"}},
		itemsByWS: map[string][]fabric.Item{
			"w1": {{ID: "g1", DisplayName: "LH_A", Type: "Lakehouse"}, {ID: "dup", DisplayName: "LH_Dup", Type: "Lakehouse"}},
			"w2": {{ID: "dup2", DisplayName: "LH_Dup", Type: "Lakehouse"}},
		},
	}
	idx, _ := BuildNameIndex(f, "tok", f.workspaces)
	if _, st := idx.LookupName("LH_A", "Lakehouse"); st != LookupFound {
		t.Errorf("LH_A status = %v, want LookupFound", st)
	}
	if _, st := idx.LookupName("LH_Missing", "Lakehouse"); st != LookupAbsent {
		t.Errorf("missing status = %v, want LookupAbsent", st)
	}
	if _, st := idx.LookupName("LH_Dup", "Lakehouse"); st != LookupAmbiguous {
		t.Errorf("dup status = %v, want LookupAmbiguous", st)
	}
}

func TestNameIndexAmbiguousNameDoesNotResolveForward(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{{ID: "ws-1", DisplayName: "W1"}, {ID: "ws-2", DisplayName: "W2"}},
		itemsByWS: map[string][]fabric.Item{
			"ws-1": {{ID: "g1", DisplayName: "LH_Dup", Type: "Lakehouse"}},
			"ws-2": {{ID: "g2", DisplayName: "LH_Dup", Type: "Lakehouse"}},
		},
	}
	idx, err := BuildNameIndex(f, "tok", f.workspaces)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, ok := idx.ItemByName("LH_Dup", "Lakehouse"); ok {
		t.Error("expected duplicate name across workspaces to be ambiguous (forward miss)")
	}
	// Reverse still works for each distinct GUID.
	if _, ok := idx.ItemByGUID("g1"); !ok {
		t.Error("reverse lookup should still resolve g1")
	}
	if _, ok := idx.ItemByGUID("g2"); !ok {
		t.Error("reverse lookup should still resolve g2")
	}
}
