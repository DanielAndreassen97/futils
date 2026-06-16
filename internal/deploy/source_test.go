package deploy

import (
	"fmt"
	"strings"
	"testing"
)

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
