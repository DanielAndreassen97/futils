package deploy

import (
	"bytes"
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
		"ls-tree -r --name-only origin/main": tree,
	}}

	batchBlobs := map[string][]byte{
		"origin/main:NB_Foo.Notebook/.platform":                   []byte(fooPlatform),
		"origin/main:NB_Foo.Notebook/notebook-content.py":         []byte("print(1)\n"),
		"origin/main:MyModel.SemanticModel/.platform":             []byte(modelPlatform),
		"origin/main:MyModel.SemanticModel/definition/model.tmdl": []byte("table X\n"),
	}
	fb := &fakeBatch{blobs: batchBlobs}

	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}

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
	// Raw .platform bytes are retained on the item (for the bulk backend).
	if len(foo.Platform) == 0 {
		t.Fatal("foo.Platform not retained")
	}
	if !strings.Contains(string(foo.Platform), `"logicalId":"aaa"`) {
		t.Errorf("foo.Platform should be raw .platform bytes, got: %s", foo.Platform)
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

// TestDiscoverItemsBatchCalledOnce proves that DiscoverItems issues exactly one
// git cat-file --batch call regardless of how many items/parts there are,
// eliminating the N+1 subprocess pattern.
func TestDiscoverItemsBatchCalledOnce(t *testing.T) {
	tree := strings.Join([]string{
		"ItemA.Notebook/.platform",
		"ItemA.Notebook/file1.py",
		"ItemA.Notebook/file2.py",
		"ItemB.SemanticModel/.platform",
		"ItemB.SemanticModel/definition/model.tmdl",
		"ItemB.SemanticModel/definition/table.tmdl",
	}, "\n") + "\n"

	platA := `{"metadata":{"type":"Notebook","displayName":"ItemA"},"config":{"logicalId":"aaa"}}`
	platB := `{"metadata":{"type":"SemanticModel","displayName":"ItemB"},"config":{"logicalId":"bbb"}}`

	g := &fakeGit{responses: map[string]string{
		"ls-tree -r --name-only origin/main": tree,
	}}
	batchBlobs := map[string][]byte{
		"origin/main:ItemA.Notebook/.platform":                  []byte(platA),
		"origin/main:ItemA.Notebook/file1.py":                   []byte("x = 1\n"),
		"origin/main:ItemA.Notebook/file2.py":                   []byte("x = 2\n"),
		"origin/main:ItemB.SemanticModel/.platform":             []byte(platB),
		"origin/main:ItemB.SemanticModel/definition/model.tmdl": []byte("model M\n"),
		"origin/main:ItemB.SemanticModel/definition/table.tmdl": []byte("table T\n"),
	}
	fb := &fakeBatch{blobs: batchBlobs}

	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	// Only ONE batch call, not 6 (one per file).
	if fb.calls != 1 {
		t.Errorf("gitBatch called %d times, want exactly 1", fb.calls)
	}

	// Verify no git show calls were made (old N+1 pattern).
	for _, call := range g.calls {
		if len(call) > 0 && call[0] == "show" {
			t.Errorf("unexpected git show call: %v (should use cat-file --batch)", call)
		}
	}
}

// TestDiscoverItemsBinaryContent proves the batch parser is binary-safe: content
// with embedded newlines and non-ASCII bytes round-trips byte-for-byte.
func TestDiscoverItemsBinaryContent(t *testing.T) {
	// Content with embedded newlines AND a non-ASCII byte (0xFF).
	multilinePart := "line one\nline two\nline three\n"
	binaryPart := "header\x00\xff\xfe data\nnewline in binary\x00tail"

	tree := strings.Join([]string{
		"Widget.Notebook/.platform",
		"Widget.Notebook/multiline.py",
		"Widget.Notebook/binary.bin",
	}, "\n") + "\n"

	plat := `{"metadata":{"type":"Notebook","displayName":"Widget"},"config":{"logicalId":"ccc"}}`

	g := &fakeGit{responses: map[string]string{
		"ls-tree -r --name-only origin/main": tree,
	}}
	batchBlobs := map[string][]byte{
		"origin/main:Widget.Notebook/.platform":    []byte(plat),
		"origin/main:Widget.Notebook/multiline.py": []byte(multilinePart),
		"origin/main:Widget.Notebook/binary.bin":   []byte(binaryPart),
	}
	fb := &fakeBatch{blobs: batchBlobs}

	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if len(items[0].Parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(items[0].Parts))
	}

	byPath := map[string][]byte{}
	for _, p := range items[0].Parts {
		byPath[p.Path] = p.Content
	}

	if !bytes.Equal(byPath["multiline.py"], []byte(multilinePart)) {
		t.Errorf("multiline.py: got %q, want %q", byPath["multiline.py"], multilinePart)
	}
	if !bytes.Equal(byPath["binary.bin"], []byte(binaryPart)) {
		t.Errorf("binary.bin: got %q, want %q", byPath["binary.bin"], binaryPart)
	}
}

// TestDiscoverItemsBucketing verifies that multi-folder discovery assigns each
// file to exactly the item folder that owns it (longest matching prefix).
func TestDiscoverItemsBucketing(t *testing.T) {
	tree := strings.Join([]string{
		"GroupA/ItemX.Notebook/.platform",
		"GroupA/ItemX.Notebook/x.py",
		"GroupA/ItemY.SemanticModel/.platform",
		"GroupA/ItemY.SemanticModel/model.tmdl",
		"GroupB/ItemZ.Notebook/.platform",
		"GroupB/ItemZ.Notebook/z.py",
		"toplevel.txt", // non-item file, must not appear anywhere
	}, "\n") + "\n"

	platX := `{"metadata":{"type":"Notebook","displayName":"ItemX"},"config":{"logicalId":"x1"}}`
	platY := `{"metadata":{"type":"SemanticModel","displayName":"ItemY"},"config":{"logicalId":"y1"}}`
	platZ := `{"metadata":{"type":"Notebook","displayName":"ItemZ"},"config":{"logicalId":"z1"}}`

	g := &fakeGit{responses: map[string]string{
		"ls-tree -r --name-only origin/main": tree,
	}}
	batchBlobs := map[string][]byte{
		"origin/main:GroupA/ItemX.Notebook/.platform":       []byte(platX),
		"origin/main:GroupA/ItemX.Notebook/x.py":            []byte("x code\n"),
		"origin/main:GroupA/ItemY.SemanticModel/.platform":  []byte(platY),
		"origin/main:GroupA/ItemY.SemanticModel/model.tmdl": []byte("model Y\n"),
		"origin/main:GroupB/ItemZ.Notebook/.platform":       []byte(platZ),
		"origin/main:GroupB/ItemZ.Notebook/z.py":            []byte("z code\n"),
	}
	fb := &fakeBatch{blobs: batchBlobs}

	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	byName := map[string]LocalItem{}
	for _, it := range items {
		byName[it.DisplayName] = it
	}

	// ItemX gets only x.py, not ItemY's model.tmdl.
	x := byName["ItemX"]
	if len(x.Parts) != 1 || x.Parts[0].Path != "x.py" {
		t.Errorf("ItemX parts = %+v", x.Parts)
	}
	// ItemY gets only model.tmdl.
	y := byName["ItemY"]
	if len(y.Parts) != 1 || y.Parts[0].Path != "model.tmdl" {
		t.Errorf("ItemY parts = %+v", y.Parts)
	}
	// ItemZ gets only z.py.
	z := byName["ItemZ"]
	if len(z.Parts) != 1 || z.Parts[0].Path != "z.py" {
		t.Errorf("ItemZ parts = %+v", z.Parts)
	}

	// Items sorted by FolderPath.
	wantOrder := []string{"GroupA/ItemX.Notebook", "GroupA/ItemY.SemanticModel", "GroupB/ItemZ.Notebook"}
	for i, item := range items {
		if item.FolderPath != wantOrder[i] {
			t.Errorf("items[%d].FolderPath = %q, want %q", i, item.FolderPath, wantOrder[i])
		}
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

// fakeBatch simulates a git cat-file --batch runner. It produces real
// cat-file --batch formatted output from the blobs map, so that
// parseCatFileBatch is exercised end-to-end.
type fakeBatch struct {
	blobs map[string][]byte // key: "<ref>:<path>"
	calls int
}

func (f *fakeBatch) run(stdin []byte, args ...string) ([]byte, error) {
	f.calls++
	var out bytes.Buffer
	for _, line := range strings.Split(strings.TrimRight(string(stdin), "\n"), "\n") {
		spec := strings.TrimSpace(line)
		if spec == "" {
			continue
		}
		data, ok := f.blobs[spec]
		if !ok {
			fmt.Fprintf(&out, "%s missing\n", spec)
			continue
		}
		// Produce real cat-file --batch header: "<oid> blob <size>\n<content>\n"
		// Use a synthetic oid for the fake.
		fmt.Fprintf(&out, "deadbeef0000000000000000000000000000000000 blob %d\n", len(data))
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func TestRepoRoot(t *testing.T) {
	g := &fakeGit{responses: map[string]string{
		"rev-parse --show-toplevel": "/repo/warehouse\n",
	}}
	if got := repoRoot(g.run); got != "/repo/warehouse" {
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

func TestSourceRepoAccessor(t *testing.T) {
	s := &Source{repo: "/repo/warehouse", ref: "origin/main"}
	if s.Repo() != "/repo/warehouse" {
		t.Errorf("Repo() = %q", s.Repo())
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

// TestDiscoverItemsExcludesGitOnlyMetadata: notebook-settings.json and
// fs-settings.json are written into git by Fabric's own sync and are outside
// the definition API's schema — they must never become definition parts
// (phantom added-part diff on every compare + unsupported file in publish
// payloads).
func TestDiscoverItemsExcludesGitOnlyMetadata(t *testing.T) {
	tree := strings.Join([]string{
		"NB_Foo.Notebook/.platform",
		"NB_Foo.Notebook/notebook-content.py",
		"NB_Foo.Notebook/notebook-settings.json",
		"NB_Foo.Notebook/fs-settings.json",
	}, "\n") + "\n"
	fooPlatform := `{"metadata":{"type":"Notebook","displayName":"NB_Foo"},"config":{"logicalId":"aaa"}}`
	g := &fakeGit{responses: map[string]string{
		"ls-tree -r --name-only origin/main": tree,
	}}
	fb := &fakeBatch{blobs: map[string][]byte{
		"origin/main:NB_Foo.Notebook/.platform":              []byte(fooPlatform),
		"origin/main:NB_Foo.Notebook/notebook-content.py":    []byte("print(1)\n"),
		"origin/main:NB_Foo.Notebook/notebook-settings.json": []byte(`{"auto-binding":{"lakehouse":"off"}}`),
		"origin/main:NB_Foo.Notebook/fs-settings.json":       []byte(`{}`),
	}}
	s := &Source{ref: "origin/main", git: g.run, gitBatch: fb.run}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 1 || len(items[0].Parts) != 1 || items[0].Parts[0].Path != "notebook-content.py" {
		t.Fatalf("want exactly the content part, got %+v", items[0].Parts)
	}
}

func TestStripScheduleParts(t *testing.T) {
	items := []LocalItem{{
		DisplayName: "PL_Main",
		Parts: []Part{
			{Path: "pipeline-content.json", Content: []byte("{}")},
			{Path: ".schedules", Content: []byte("{}")},
		},
	}}
	out := StripScheduleParts(items)
	if len(out[0].Parts) != 1 || out[0].Parts[0].Path != "pipeline-content.json" {
		t.Fatalf("schedules not stripped: %+v", out[0].Parts)
	}
	// The input items must not be mutated (callers may reuse them).
	if len(items[0].Parts) != 2 {
		t.Fatalf("input mutated: %+v", items[0].Parts)
	}
}
