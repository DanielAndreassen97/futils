package deploy

import "testing"

func idx(items ...IndexedItem) *NameIndex {
	i := &NameIndex{byGUID: map[string]IndexedItem{}, byName: map[nameKey]IndexedItem{}, ambiguous: map[nameKey]bool{}}
	for _, it := range items {
		i.Add(it)
	}
	return i
}

func TestBuildWorkspaceMapUnanimous(t *testing.T) {
	base := idx(
		IndexedItem{Name: "PL_Copy", Type: "DataPipeline", GUID: "b1", WorkspaceID: "ws-dev"},
		IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: "b2", WorkspaceID: "ws-dev"},
	)
	tgt := idx(
		IndexedItem{Name: "PL_Copy", Type: "DataPipeline", GUID: "t1", WorkspaceID: "ws-test"},
		IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: "t2", WorkspaceID: "ws-test"},
	)
	m, amb := buildWorkspaceMap(base, tgt)
	if m["ws-dev"] != "ws-test" || len(amb) != 0 {
		t.Fatalf("m=%v amb=%v", m, amb)
	}
}

func TestBuildWorkspaceMapConflictIsAmbiguous(t *testing.T) {
	base := idx(
		IndexedItem{Name: "A", Type: "Notebook", GUID: "b1", WorkspaceID: "ws-dev"},
		IndexedItem{Name: "B", Type: "Notebook", GUID: "b2", WorkspaceID: "ws-dev"},
	)
	tgt := idx(
		IndexedItem{Name: "A", Type: "Notebook", GUID: "t1", WorkspaceID: "ws-test-1"},
		IndexedItem{Name: "B", Type: "Notebook", GUID: "t2", WorkspaceID: "ws-test-2"},
	)
	m, amb := buildWorkspaceMap(base, tgt)
	if _, ok := m["ws-dev"]; ok || !amb["ws-dev"] {
		t.Fatalf("conflicting votes must be ambiguous: m=%v amb=%v", m, amb)
	}
}

func TestBuildWorkspaceMapNoVotes(t *testing.T) {
	base := idx(IndexedItem{Name: "Only", Type: "Notebook", GUID: "b1", WorkspaceID: "ws-dev"})
	tgt := idx() // nothing resolves
	m, amb := buildWorkspaceMap(base, tgt)
	if len(m) != 0 || len(amb) != 0 {
		t.Fatalf("no votes must produce no entries: m=%v amb=%v", m, amb)
	}
}

func TestSetWorkspaceSeedsWinsOverConsensus(t *testing.T) {
	rb := &Rebinder{wsMap: map[string]string{"ws-dev": "wrong"}, wsAmbiguous: map[string]bool{"ws-empty": true}}
	rb.SetWorkspaceSeeds(map[string]string{"ws-dev": "ws-test", "ws-empty": "ws-test-2"})
	if rb.wsMap["ws-dev"] != "ws-test" || rb.wsMap["ws-empty"] != "ws-test-2" || rb.wsAmbiguous["ws-empty"] {
		t.Fatalf("seeds must overwrite: %v %v", rb.wsMap, rb.wsAmbiguous)
	}
}
