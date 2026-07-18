package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDiscoverItemsRealGit proves the working tree is never read: we commit
// an item to a branch, check out a different branch, dirty the working tree,
// and confirm discovery still reflects the committed state.
func TestDiscoverItemsRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	itemDir := filepath.Join(repo, "NB_Foo.Notebook")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, ".platform"),
		[]byte(`{"metadata":{"type":"Notebook","displayName":"NB_Foo"},"config":{"logicalId":"aaa"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "notebook-content.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "add foo")

	// Simulate a feature branch with uncommitted edits.
	run("checkout", "-b", "feature/x")
	if err := os.WriteFile(filepath.Join(itemDir, "notebook-content.py"), []byte("print(999)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Read from the main branch directly (no origin in this local repo).
	s := &Source{repo: repo, ref: "main", git: realGitRunner(repo), gitBatch: realGitBatchRunner(repo)}
	items, err := s.DiscoverItems()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(items) != 1 || items[0].DisplayName != "NB_Foo" {
		t.Fatalf("items = %+v", items)
	}
	if string(items[0].Parts[0].Content) != "print(1)\n" {
		t.Errorf("read working-tree content %q; must read committed content", items[0].Parts[0].Content)
	}
}

// TestNewSourceAtPinnedBranch: an explicit branch must skip default-branch
// detection entirely — a repo whose origin has no main/master (only a pinned
// branch like dev) is otherwise unusable.
func TestNewSourceAtPinnedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "dev")

	// Auto-detection has nothing to find: no origin at all — and a pinned
	// branch doesn't excuse a missing remote either.
	if _, err := NewSource(repo); err == nil {
		t.Fatal("NewSource should fail without origin/main or origin/master")
	}
	if _, err := NewSourceAt(repo, "dev"); err == nil {
		t.Fatal("NewSourceAt should fail when the repo has no origin remote")
	}

	// With an origin whose only branch is dev, the pin resolves — while
	// auto-detection still has no main/master to find.
	origin := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "-q", "--bare", origin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	run("remote", "add", "origin", origin)

	s, err := NewSourceAt(repo, "dev")
	if err != nil {
		t.Fatalf("NewSourceAt: %v", err)
	}
	if s.Ref() != "origin/dev" {
		t.Errorf("Ref() = %q, want origin/dev", s.Ref())
	}
}

// TestListRemoteBranches: branches on the origin remote are listed by name
// (no origin/ prefix), sorted — including branches never fetched locally,
// which is the whole point (a Fabric workspace commits straight to the
// remote).
func TestListRemoteBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "seed")

	origin := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "-q", "--bare", origin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	run("remote", "add", "origin", origin)
	run("push", "-q", "origin", "main")
	run("push", "-q", "origin", "main:feature/daniel") // exists ONLY on origin

	got, err := ListRemoteBranches(repo)
	if err != nil {
		t.Fatalf("ListRemoteBranches: %v", err)
	}
	want := []string{"feature/daniel", "main"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("branches = %v, want %v", got, want)
	}
}
