package deploy

import (
	"fmt"
	"os/exec"
	"strings"
)

// gitRunner runs a git subcommand and returns stdout. The real one targets a
// repo dir via `git -C <repo>`. Tests swap in a fake.
type gitRunner func(args ...string) ([]byte, error)

func realGitRunner(repo string) gitRunner {
	return func(args ...string) ([]byte, error) {
		full := append([]string{"-C", repo}, args...)
		out, err := exec.Command("git", full...).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
}

// Source reads Fabric items from a single git ref (always origin/<default>).
type Source struct {
	repo string
	ref  string // e.g. "origin/main"
	git  gitRunner
}

// NewSource validates the repo and resolves the deploy ref to origin/<default>.
func NewSource(repo string) (*Source, error) {
	g := realGitRunner(repo)
	if _, err := g("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%q is not a git repository: %w", repo, err)
	}
	branch, err := detectDefaultBranch(g)
	if err != nil {
		return nil, err
	}
	return &Source{repo: repo, ref: "origin/" + branch, git: g}, nil
}

// detectDefaultBranch resolves origin's HEAD (e.g. main), falling back to
// probing origin/main then origin/master if HEAD isn't set locally.
func detectDefaultBranch(g gitRunner) (string, error) {
	if out, err := g("symbolic-ref", "refs/remotes/origin/HEAD"); err == nil {
		ref := strings.TrimSpace(string(out)) // refs/remotes/origin/main
		if i := strings.LastIndex(ref, "/"); i >= 0 {
			return ref[i+1:], nil
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := g("rev-parse", "--verify", "origin/"+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("could not determine default branch (no origin/main or origin/master)")
}

// Ref returns the resolved deploy ref, e.g. "origin/main".
func (s *Source) Ref() string { return s.ref }

// Fetch updates remote-tracking refs so the deploy ref reflects the latest
// merged state. Always run before discovery.
func (s *Source) Fetch() error {
	if _, err := s.git("fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch failed (check network and your git credentials): %w", err)
	}
	return nil
}

// ReadFile returns the bytes of a repo-relative path at the deploy ref.
func (s *Source) ReadFile(path string) ([]byte, error) {
	out, err := s.git("show", s.ref+":"+path)
	if err != nil {
		return nil, fmt.Errorf("read %s@%s: %w", path, s.ref, err)
	}
	return out, nil
}
