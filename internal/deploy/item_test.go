package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePlatform(t *testing.T) {
	raw := []byte(`{
	  "metadata": { "type": "Notebook", "displayName": "NB_Foo", "description": "does foo" },
	  "config": { "logicalId": "11111111-1111-1111-1111-111111111111" }
	}`)
	meta, err := parsePlatform(raw)
	if err != nil {
		t.Fatalf("parsePlatform: %v", err)
	}
	if meta.Type != "Notebook" || meta.DisplayName != "NB_Foo" {
		t.Errorf("got %+v", meta)
	}
	if meta.LogicalID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("logicalId = %q", meta.LogicalID)
	}
	if meta.Description != "does foo" {
		t.Errorf("description = %q", meta.Description)
	}
}

func TestParsePlatformRejectsMissingType(t *testing.T) {
	if _, err := parsePlatform([]byte(`{"metadata":{"displayName":"X"}}`)); err == nil {
		t.Fatal("expected error for missing type")
	}
}

func writePlatform(t *testing.T, dir, folder, itemType string) {
	t.Helper()
	fp := filepath.Join(dir, folder)
	if err := os.MkdirAll(fp, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"metadata":{"type":%q,"displayName":%q}}`, itemType, folder)
	if err := os.WriteFile(filepath.Join(fp, ".platform"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepoItemTypes(t *testing.T) {
	dir := t.TempDir()
	writePlatform(t, dir, "NB_A.Notebook", "Notebook")
	writePlatform(t, dir, "PL_A.DataPipeline", "DataPipeline")
	writePlatform(t, dir, "NB_B.Notebook", "Notebook") // duplicate type
	if err := os.MkdirAll(filepath.Join(dir, "not-an-item"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := RepoItemTypes(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"DataPipeline", "Notebook"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRepoItemTypesMissingPath(t *testing.T) {
	got, err := RepoItemTypes(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil || len(got) != 0 {
		t.Errorf("missing path: got %v err %v, want empty/no-error", got, err)
	}
}

// writeTestPlatform drops a minimal .platform file for one item folder.
func writeTestPlatform(t *testing.T, root, folder, itemType, name string) {
	t.Helper()
	dir := filepath.Join(root, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"metadata":{"type":"` + itemType + `","displayName":"` + name + `"}}`
	if err := os.WriteFile(filepath.Join(dir, ".platform"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepoItemNames(t *testing.T) {
	root := t.TempDir()
	writeTestPlatform(t, root, "NB_B.Notebook", "Notebook", "NB_B")
	writeTestPlatform(t, root, "NB_A.Notebook", "Notebook", "NB_A")
	writeTestPlatform(t, root, "LH_X.Lakehouse", "Lakehouse", "LH_X")

	names, err := RepoItemNames(root, "Notebook")
	if err != nil {
		t.Fatalf("RepoItemNames: %v", err)
	}
	want := []string{"NB_A", "NB_B"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestRepoItemNamesEmptyPath(t *testing.T) {
	names, err := RepoItemNames("", "Notebook")
	if err != nil || names != nil {
		t.Fatalf("got (%v, %v), want (nil, nil)", names, err)
	}
}

func TestRepoItemNamesMultiUnions(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	writeTestPlatform(t, r1, "NB_A.Notebook", "Notebook", "NB_A")
	writeTestPlatform(t, r2, "NB_B.Notebook", "Notebook", "NB_B")
	writeTestPlatform(t, r2, "NB_A.Notebook", "Notebook", "NB_A") // dup across repos
	got, err := RepoItemNamesMulti([]string{r1, r2}, "Notebook")
	if err != nil {
		t.Fatalf("RepoItemNamesMulti: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"NB_A", "NB_B"}) {
		t.Fatalf("got %v, want [NB_A NB_B]", got)
	}
}

func TestRepoItemTypesMultiSkipsEmpty(t *testing.T) {
	r1 := t.TempDir()
	writeTestPlatform(t, r1, "LH.Lakehouse", "Lakehouse", "LH")
	got, err := RepoItemTypesMulti([]string{"", r1, ""}) // empty paths skipped, not errors
	if err != nil {
		t.Fatalf("RepoItemTypesMulti: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"Lakehouse"}) {
		t.Fatalf("got %v, want [Lakehouse]", got)
	}
}
