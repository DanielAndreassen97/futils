package deploy

import (
	"reflect"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestWorkspaceFolderPath(t *testing.T) {
	cases := []struct{ folderPath, root, want string }{
		{"Backend/X.Notebook", "Backend", ""},
		{"Backend/Sub/X.Notebook", "Backend", "Sub"},
		{"Backend/Notebooks/Config/NB.Notebook", "Backend", "Notebooks/Config"},
		{"/Backend/Sub/X.Notebook/", "Backend", "Sub"},
		{"Top/A/B/X.Report", "", "Top/A/B"},
		{"X.Notebook", "", ""},
		{"Other/X.Notebook", "Backend", ""}, // not under root → root
	}
	for _, c := range cases {
		if got := WorkspaceFolderPath(c.folderPath, c.root); got != c.want {
			t.Errorf("WorkspaceFolderPath(%q, %q) = %q, want %q", c.folderPath, c.root, got, c.want)
		}
	}
}

func TestFolderFullPaths(t *testing.T) {
	folders := []fabric.Folder{
		{ID: "f1", DisplayName: "Notebooks"},
		{ID: "f2", DisplayName: "Config", ParentFolderID: "f1"},
		{ID: "f3", DisplayName: "Pipelines"},
		{ID: "f4", DisplayName: "Orphan", ParentFolderID: "missing"},
	}
	got := folderFullPaths(folders)
	want := map[string]string{
		"Notebooks":        "f1",
		"Notebooks/Config": "f2",
		"Pipelines":        "f3",
		// f4 skipped: parent chain unresolved
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folderFullPaths = %v, want %v", got, want)
	}
}

func TestNeededFolderPaths(t *testing.T) {
	got := neededFolderPaths([]string{"Notebooks/Config", "Notebooks", "", "Pipelines/Main", "Notebooks/Config"})
	// Shallowest-first, ancestors included, deduped.
	want := []string{"Notebooks", "Pipelines", "Notebooks/Config", "Pipelines/Main"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("neededFolderPaths = %v, want %v", got, want)
	}
}
