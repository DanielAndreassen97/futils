package deploy

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiscoverItems(t *testing.T) {
	tree := strings.Join([]string{
		"parameter.yml",
		"NB_Foo.Notebook/.platform",
		"NB_Foo.Notebook/notebook-content.py",
		"MyModel.SemanticModel/.platform",
		"MyModel.SemanticModel/definition/model.tmdl",
		"README.md",
	}, "\n") + "\n"

	fooPlatform := `{"metadata":{"type":"Notebook","displayName":"NB_Foo"},"config":{"logicalId":"aaa"}}`
	modelPlatform := `{"metadata":{"type":"SemanticModel","displayName":"MyModel"},"config":{"logicalId":"bbb"}}`

	g := &fakeGit{responses: map[string]string{
		"ls-tree -r --name-only origin/main":                              tree,
		"show origin/main:NB_Foo.Notebook/.platform":                     fooPlatform,
		"show origin/main:NB_Foo.Notebook/notebook-content.py":           "print(1)\n",
		"show origin/main:MyModel.SemanticModel/.platform":                modelPlatform,
		"show origin/main:MyModel.SemanticModel/definition/model.tmdl":   "table X\n",
	}}
	s := &Source{ref: "origin/main", git: g.run}

	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	byName := map[string]LocalItem{}
	for _, it := range items {
		byName[it.DisplayName] = it
	}
	foo := byName["NB_Foo"]
	if foo.Type != "Notebook" || foo.LogicalID != "aaa" || foo.FolderPath != "NB_Foo.Notebook" {
		t.Errorf("foo = %+v", foo)
	}
	// .platform is excluded from parts; the single content file remains.
	if len(foo.Parts) != 1 || foo.Parts[0].Path != "notebook-content.py" {
		t.Errorf("foo parts = %+v", foo.Parts)
	}
	if string(foo.Parts[0].Content) != "print(1)\n" {
		t.Errorf("foo content = %q", foo.Parts[0].Content)
	}
	// Nested part path is relative to the item folder.
	model := byName["MyModel"]
	if len(model.Parts) != 1 || model.Parts[0].Path != "definition/model.tmdl" {
		t.Errorf("model parts = %+v", model.Parts)
	}
}

// fakeGit records calls and returns canned output keyed by the joined args.
type fakeGit struct {
	responses map[string]string // key: strings.Join(args, " ")
	calls     [][]string
}

func (f *fakeGit) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	key := strings.Join(args, " ")
	out, ok := f.responses[key]
	if !ok {
		return nil, fmt.Errorf("fakeGit: no canned response for %q", key)
	}
	return []byte(out), nil
}

func TestRepoRoot(t *testing.T) {
	g := &fakeGit{responses: map[string]string{
		"rev-parse --show-toplevel": "/Users/dan/Repos/Datavarehus\n",
	}}
	if got := repoRoot(g.run); got != "/Users/dan/Repos/Datavarehus" {
		t.Errorf("repoRoot = %q", got)
	}
}

func TestDetectDefaultBranch(t *testing.T) {
	g := &fakeGit{responses: map[string]string{
		"symbolic-ref refs/remotes/origin/HEAD": "refs/remotes/origin/main\n",
	}}
	branch, err := detectDefaultBranch(g.run)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
}

func TestReadFileAtRef(t *testing.T) {
	g := &fakeGit{responses: map[string]string{
		"show origin/main:parameter.yml": "find_replace: []\n",
	}}
	s := &Source{ref: "origin/main", git: g.run}
	got, err := s.ReadFile("parameter.yml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "find_replace: []\n" {
		t.Errorf("got %q", got)
	}
}
