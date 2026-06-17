package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsGitRepo(t *testing.T) {
	dir := t.TempDir()
	if isGitRepo(dir) {
		t.Fatal("empty temp dir should not be a git repo")
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !isGitRepo(dir) {
		t.Fatal("dir with .git should be a git repo")
	}
}

func TestNextDir(t *testing.T) {
	if next, done := nextDir("/a/b", dirSelectValue); !done || next != "/a/b" {
		t.Errorf("select: got (%q,%v)", next, done)
	}
	if next, done := nextDir("/a/b", dirUpValue); done || next != "/a" {
		t.Errorf("up: got (%q,%v)", next, done)
	}
	if next, done := nextDir("/a/b", "c"); done || next != "/a/b/c" {
		t.Errorf("descend: got (%q,%v)", next, done)
	}
}

func TestDirOptionsSkipsHiddenAndMarksGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repoA", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, err := dirOptions(root)
	if err != nil {
		t.Fatalf("dirOptions: %v", err)
	}
	if opts[0].Value != dirSelectValue {
		t.Errorf("first option should be select, got %q", opts[0].Value)
	}
	byLabel := map[string]FilterOption{}
	for _, o := range opts {
		byLabel[o.Label] = o
	}
	if _, ok := byLabel[".hidden"]; ok {
		t.Error("hidden dir should be skipped")
	}
	if byLabel["repoA"].Meta != "git" {
		t.Errorf("repoA should be marked git, got Meta=%v", byLabel["repoA"].Meta)
	}
	if byLabel["plain"].Meta != "" {
		t.Errorf("plain should not be marked git, got Meta=%v", byLabel["plain"].Meta)
	}
}
