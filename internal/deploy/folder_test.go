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
