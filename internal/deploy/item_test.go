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
