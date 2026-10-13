package deploy

import "testing"

func TestItemsInFolder(t *testing.T) {
	items := []LocalItem{
		{DisplayName: "A", FolderPath: "Backend/A.Notebook"},
		{DisplayName: "B", FolderPath: "Backend/sub/B.Notebook"},
		{DisplayName: "C", FolderPath: "Frontend/C.Report"},
		{DisplayName: "D", FolderPath: "BackendExtra/D.Notebook"}, // must NOT match "Backend"
	}
	got := ItemsInFolder(items, "Backend")
	if len(got) != 2 {
		t.Fatalf("want 2 items under Backend, got %d: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, it := range got {
		names[it.DisplayName] = true
	}
	if !names["A"] || !names["B"] || names["C"] || names["D"] {
		t.Errorf("wrong matches: %v", names)
	}
}

func TestItemsInFolderNormalizesSlashes(t *testing.T) {
	items := []LocalItem{{DisplayName: "A", FolderPath: "Backend/A.Notebook"}}
	for _, folder := range []string{"Backend", "/Backend", "Backend/", "/Backend/"} {
		if got := ItemsInFolder(items, folder); len(got) != 1 {
			t.Errorf("folder %q: want 1, got %d", folder, len(got))
		}
	}
}

func TestItemsInFolderEmptyMatchesAll(t *testing.T) {
	items := []LocalItem{{FolderPath: "Backend/A.Notebook"}, {FolderPath: "Frontend/B.Report"}}
	if got := ItemsInFolder(items, ""); len(got) != 2 {
		t.Errorf("empty folder should match all, got %d", len(got))
	}
}

func TestTopLevelFolders(t *testing.T) {
	items := []LocalItem{
		{FolderPath: "FabricBackEnd/NB_A.Notebook"},
		{FolderPath: "FabricBackEnd/sub/NB_B.Notebook"},
		{FolderPath: "FabricFrontEnd/R.Report"},
		{FolderPath: "RootItem.Notebook"}, // at repo root → skipped
	}
	got := TopLevelFolders(items)
	if len(got) != 2 || got[0] != "FabricBackEnd" || got[1] != "FabricFrontEnd" {
		t.Errorf("got %v, want [FabricBackEnd FabricFrontEnd]", got)
	}
}

// Deploy setup offers "split into subfolders" from this, and warns about the
// direct items no subfolder mapping would cover: a wrong count there means an
// item silently never deploys.
func TestSubFolders(t *testing.T) {
	items := []LocalItem{
		{FolderPath: "FrontEnd/FullModel.SemanticModel"}, // direct in FrontEnd
		{FolderPath: "FrontEnd/Reports/R1.Report"},
		{FolderPath: "FrontEnd/Reports/Sales/R2.Report"},
		{FolderPath: "FrontEnd/SemanticModels/M.SemanticModel"},
		{FolderPath: "FrontEndOld/X.Report"}, // must NOT count as FrontEnd
		{FolderPath: "BackEnd/NB.Notebook"},
	}
	subs, direct := SubFolders(items, "/FrontEnd/")
	if len(subs) != 2 || subs[0] != "FrontEnd/Reports" || subs[1] != "FrontEnd/SemanticModels" {
		t.Errorf("subs = %v, want [FrontEnd/Reports FrontEnd/SemanticModels]", subs)
	}
	if direct != 1 {
		t.Errorf("direct = %d, want 1", direct)
	}

	subs, direct = SubFolders(items, "FrontEnd/Reports")
	if len(subs) != 1 || subs[0] != "FrontEnd/Reports/Sales" || direct != 1 {
		t.Errorf("Reports: subs = %v direct = %d, want [FrontEnd/Reports/Sales] 1", subs, direct)
	}
}
