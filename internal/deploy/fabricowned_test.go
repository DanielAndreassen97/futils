package deploy

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestIsFabricOwnedPart(t *testing.T) {
	cases := []struct {
		itemType, path string
		want           bool
	}{
		// The live case: Power BI Desktop writes this, git never carries it, and
		// getDefinition returns it anyway.
		{"SemanticModel", ".pbi/editorSettings.json", true},
		{"SemanticModel", "definition/tables/Dim Ansatt.tmdl", false},
		{"SemanticModel", "definition/model.tmdl", false},
		{"Report", ".pbi/localSettings.json", true},
		{"DataAgent", ".pbi/anything.json", true},
		{"Eventhouse", ".children/db.json", true},
		{"Eventhouse", ".pbi/x.json", false}, // .pbi is not the eventhouse's folder
		// Segment match anywhere in the path, matching fabric-cicd's regex reach.
		{"SemanticModel", "definition/.pbi/nested.json", true},
		// A folder that merely starts with the name is not the folder.
		{"SemanticModel", ".pbix/thing.json", false},
		{"SemanticModel", "definition/.pbi.tmdl", false},
		// Types with no Fabric-owned folder are never filtered.
		{"Notebook", ".pbi/editorSettings.json", false},
		{"Lakehouse", ".children/x", false},
	}
	for _, c := range cases {
		if got := IsFabricOwnedPart(c.itemType, c.path); got != c.want {
			t.Errorf("IsFabricOwnedPart(%q, %q) = %v, want %v", c.itemType, c.path, got, c.want)
		}
	}
}

// The deployed side must be filtered with the same rule as discovery. Without
// this, .pbi/editorSettings.json shows up as a part the publish deletes on every
// single semantic-model deploy — a permanent false positive on the part-removal
// gate.
func TestDropFabricOwnedParts(t *testing.T) {
	parts := []fabric.DefinitionPart{
		{Path: "definition/model.tmdl"},
		{Path: ".pbi/editorSettings.json"},
		{Path: "definition/tables/Dim Ansatt.tmdl"},
	}
	got := DropFabricOwnedParts("SemanticModel", parts)
	if len(got) != 2 {
		t.Fatalf("got %d parts, want 2: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Path == ".pbi/editorSettings.json" {
			t.Error("Fabric-owned part survived the filter")
		}
	}
	// An item type with no owned folder keeps every part, same slice.
	if got := DropFabricOwnedParts("Notebook", parts); len(got) != 3 {
		t.Errorf("Notebook parts must pass through untouched, got %d", len(got))
	}
}

// Discovery must not pick .pbi files up from git either — a repo that DOES
// carry them would otherwise publish them straight back into the payload.
func TestDiscoverItemsDropsFabricOwnedParts(t *testing.T) {
	tree := strings.Join([]string{
		"DW - Salg.SemanticModel/.platform",
		"DW - Salg.SemanticModel/definition/model.tmdl",
		"DW - Salg.SemanticModel/.pbi/editorSettings.json",
		"DW - Salg.SemanticModel/.pbi/localSettings.json",
	}, "\x00") + "\x00"
	plat := `{"metadata":{"type":"SemanticModel","displayName":"DW - Salg"},"config":{"logicalId":"aaa"}}`
	g := &fakeGit{responses: map[string]string{
		"ls-tree -r -z --name-only origin/main": tree,
	}}
	fb := &fakeBatch{blobs: map[string][]byte{
		"origin/main:DW - Salg.SemanticModel/.platform":                []byte(plat),
		"origin/main:DW - Salg.SemanticModel/definition/model.tmdl":    []byte("model M\n"),
		"origin/main:DW - Salg.SemanticModel/.pbi/editorSettings.json": []byte("{}"),
		"origin/main:DW - Salg.SemanticModel/.pbi/localSettings.json":  []byte("{}"),
	}}
	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if len(items[0].Parts) != 1 || items[0].Parts[0].Path != "definition/model.tmdl" {
		t.Errorf("parts = %+v, want only definition/model.tmdl", items[0].Parts)
	}
}
