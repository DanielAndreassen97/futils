package deploy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

// gitRunner runs a git subcommand and returns stdout. The real one targets a
// repo dir via `git -C <repo>`. Tests swap in a fake.
type gitRunner func(args ...string) ([]byte, error)

// gitBatchRunner runs a git subcommand with data piped on stdin and returns
// stdout. Used for git cat-file --batch to read many blobs in one subprocess.
type gitBatchRunner func(stdin []byte, args ...string) ([]byte, error)

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

func realGitBatchRunner(repo string) gitBatchRunner {
	return func(stdin []byte, args ...string) ([]byte, error) {
		full := append([]string{"-C", repo}, args...)
		cmd := exec.Command("git", full...)
		cmd.Stdin = bytes.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
}

// ListRemoteBranches returns origin's branch names (without any origin/
// prefix), sorted. It asks the remote directly (git ls-remote) so branches
// pushed from elsewhere — e.g. a Fabric workspace committing straight to
// DevOps — show up without a local fetch; when the remote is unreachable it
// falls back to the locally-known origin refs.
func ListRemoteBranches(repoPath string) ([]string, error) {
	git := realGitRunner(repoPath)
	var branches []string
	if out, err := git("ls-remote", "--heads", "origin"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if _, ref, ok := strings.Cut(line, "\trefs/heads/"); ok {
				branches = append(branches, strings.TrimSpace(ref))
			}
		}
	}
	if len(branches) == 0 {
		out, err := git("for-each-ref", "--format=%(refname:short)", "refs/remotes/origin")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			name := strings.TrimPrefix(strings.TrimSpace(line), "origin/")
			if name == "" || name == "HEAD" {
				continue
			}
			branches = append(branches, name)
		}
	}
	sort.Strings(branches)
	return branches, nil
}

// Source reads Fabric items from a single git ref (always origin/<default>).
type Source struct {
	repo     string
	ref      string // e.g. "origin/main"
	git      gitRunner
	gitBatch gitBatchRunner
}

// NewSource validates the repo and resolves the deploy ref to origin/<default>.
func NewSource(repo string) (*Source, error) { return NewSourceAt(repo, "") }

// NewSourceAt is NewSource with an explicit branch: a non-empty branch pins
// the deploy ref to origin/<branch> (a customer deploying from origin/dev, a
// release branch, or a default branch that isn't main/master), skipping
// default-branch detection entirely. The pinned ref may not exist locally yet
// — Fetch verifies it once the remote has been consulted.
func NewSourceAt(repo, branch string) (*Source, error) {
	g := realGitRunner(repo)
	if _, err := g("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%q is not inside a git repository: %w", repo, err)
	}
	if root := repoRoot(g); root != "" && root != repo {
		repo = root
		g = realGitRunner(root)
	}
	if branch == "" {
		detected, err := detectDefaultBranch(g)
		if err != nil {
			return nil, err
		}
		branch = detected
	} else if _, err := g("remote", "get-url", "origin"); err != nil {
		// A pinned branch skips default-branch detection — the only other
		// check that an origin exists at all — so assert the remote here.
		return nil, fmt.Errorf("%q has no 'origin' remote — deploys read from origin/<branch>", repo)
	}
	return &Source{
		repo:     repo,
		ref:      "origin/" + branch,
		git:      g,
		gitBatch: realGitBatchRunner(repo),
	}, nil
}

// repoRoot returns the repository's top-level directory via the runner, or ""
// if it can't be determined. Used so a path pointing inside a repo (a
// subfolder) resolves to the root — ls-tree/show then agree on root-relative
// paths.
func repoRoot(g gitRunner) string {
	out, err := g("rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

// Repo returns the resolved repository root (git top-level) this source reads
// from. Used to persist the path so the picker can be skipped next time.
func (s *Source) Repo() string { return s.repo }

// Fetch updates remote-tracking refs so the deploy ref reflects the latest
// merged state, then verifies the ref actually exists — the clear error beats
// the git-flavored one discovery would produce for a mistyped or deleted
// branch. Always run before discovery.
func (s *Source) Fetch() error {
	if _, err := s.git("fetch", "origin"); err != nil {
		return fmt.Errorf("git fetch failed (check network and your git credentials): %w", err)
	}
	if _, err := s.git("rev-parse", "--verify", s.ref); err != nil {
		return fmt.Errorf("deploy ref %s not found after fetch — check that the branch exists on origin and that this clone fetches it (a single-branch clone only fetches one)", s.ref)
	}
	return nil
}

// ReadFile returns the bytes of a repo-relative path at the deploy ref.
func (s *Source) ReadFile(p string) ([]byte, error) {
	out, err := s.git("show", s.ref+":"+p)
	if err != nil {
		return nil, fmt.Errorf("read %s@%s: %w", p, s.ref, err)
	}
	return out, nil
}

// DiscoverItems lists every tree path at the deploy ref, treats each folder
// containing a .platform as an item, and reads that folder's files (excluding
// .platform) as definition parts. Part paths are relative to the item folder.
//
// Efficiency: files are bucketed into item folders in a single O(files×folders)
// pass, and all blob content is fetched in one git cat-file --batch subprocess
// call, eliminating the N+1 git show pattern.
func (s *Source) DiscoverItems() ([]LocalItem, error) {
	out, err := s.git("ls-tree", "-r", "--name-only", s.ref)
	if err != nil {
		return nil, fmt.Errorf("list tree: %w", err)
	}
	var all []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			all = append(all, line)
		}
	}

	// Identify item folders (any directory that contains a .platform file).
	itemFolders := map[string]bool{}
	for _, p := range all {
		if path.Base(p) == ".platform" {
			itemFolders[path.Dir(p)] = true
		}
	}

	// Single-pass bucketing: assign each file to the item folder whose prefix
	// it matches. When item folders can be nested, pick the longest match —
	// identical to the original strings.HasPrefix(p, folder+"/") semantics.
	filesByFolder := make(map[string][]string, len(itemFolders))
	for _, p := range all {
		best := ""
		for folder := range itemFolders {
			prefix := folder + "/"
			if strings.HasPrefix(p, prefix) && len(folder) > len(best) {
				best = folder
			}
		}
		if best != "" {
			filesByFolder[best] = append(filesByFolder[best], p)
		}
	}

	// Build the ordered spec list for git cat-file --batch. Specs are ordered:
	// for each item folder (in deterministic sorted order) we emit the
	// .platform spec first, then the part file specs in all-list order.
	sortedFolders := make([]string, 0, len(itemFolders))
	for folder := range itemFolders {
		sortedFolders = append(sortedFolders, folder)
	}
	sort.Strings(sortedFolders)

	var specs []string
	for _, folder := range sortedFolders {
		specs = append(specs, s.ref+":"+folder+"/.platform")
		for _, p := range filesByFolder[folder] {
			if path.Base(p) != ".platform" {
				specs = append(specs, s.ref+":"+p)
			}
		}
	}

	// Fetch all blobs in one subprocess call.
	blobs, err := s.batchReadBlobs(specs)
	if err != nil {
		return nil, err
	}

	var items []LocalItem
	for _, folder := range sortedFolders {
		platSpec := s.ref + ":" + folder + "/.platform"
		platRaw, ok := blobs[platSpec]
		if !ok {
			return nil, fmt.Errorf("read %s: blob missing from batch output", platSpec)
		}
		meta, err := parsePlatform(platRaw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", folder, err)
		}
		item := LocalItem{
			Type:            meta.Type,
			DisplayName:     meta.DisplayName,
			Description:     meta.Description,
			LogicalID:       meta.LogicalID,
			FolderPath:      folder,
			Platform:        platRaw,
			CreationPayload: meta.CreationPayload,
		}
		for _, p := range filesByFolder[folder] {
			rel := strings.TrimPrefix(p, folder+"/")
			// .platform is consumed separately (metadata + bulk payload).
			// notebook-settings.json / fs-settings.json are written into git by
			// Fabric's own sync (auto-binding state) and are OUTSIDE the
			// definition API's schema: getDefinition never returns them and
			// updateDefinition has no slot for them, so keeping them as parts
			// yields a phantom added-part diff on every compare and rides an
			// unsupported file into every publish payload (fabric-cicd#883
			// hard-failed on exactly this).
			if rel == ".platform" || rel == "notebook-settings.json" || rel == "fs-settings.json" {
				continue
			}
			spec := s.ref + ":" + p
			content, ok := blobs[spec]
			if !ok {
				return nil, fmt.Errorf("read %s: blob missing from batch output", spec)
			}
			item.Parts = append(item.Parts, Part{Path: rel, Content: content})
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].FolderPath < items[j].FolderPath })
	return items, nil
}

// batchReadBlobs fetches the content of all given object specs
// (<ref>:<path> format) using one git cat-file --batch subprocess.
// Output is matched to specs by position (git cat-file --batch outputs
// results in the same order as the input).
func (s *Source) batchReadBlobs(specs []string) (map[string][]byte, error) {
	if len(specs) == 0 {
		return map[string][]byte{}, nil
	}

	stdin := []byte(strings.Join(specs, "\n") + "\n")

	raw, runErr := s.gitBatch(stdin, "cat-file", "--batch")
	if runErr != nil {
		return nil, fmt.Errorf("git cat-file --batch: %w", runErr)
	}

	return parseCatFileBatchOrdered(raw, specs)
}

// parseCatFileBatchOrdered parses git cat-file --batch output, matching
// results to specs by position (output order == input order).
//
// Binary-safety: content bytes are read via io.ReadFull(size), never by
// splitting on newlines, so embedded newlines and null bytes survive intact.
//
// Format per object:
//
//	found:   "<oid> <type> <size>\n" + exactly <size> raw content bytes + "\n"
//	missing: "<spec> missing\n"      (no content bytes follow)
func parseCatFileBatchOrdered(data []byte, specs []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(specs))
	r := bufio.NewReader(bytes.NewReader(data))

	for _, spec := range specs {
		header, err := r.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cat-file parse: read header for %q: %w", spec, err)
		}
		header = strings.TrimRight(header, "\n")

		// Missing object: leave key absent — caller detects the gap.
		if strings.HasSuffix(header, " missing") {
			continue
		}

		// Found: "<oid> <type> <size>"
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return nil, fmt.Errorf("cat-file parse: unexpected header %q for spec %q", header, spec)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("cat-file parse: bad size %q in header for spec %q", fields[2], spec)
		}

		// Read exactly <size> bytes — binary-safe.
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			return nil, fmt.Errorf("cat-file parse: read %d bytes for spec %q: %w", size, spec, err)
		}

		// Consume the trailing newline git appends after content.
		if b, err := r.ReadByte(); err != nil && err != io.EOF {
			return nil, fmt.Errorf("cat-file parse: trailing newline for spec %q: %w", spec, err)
		} else if err == nil && b != '\n' {
			return nil, fmt.Errorf("cat-file parse: expected trailing newline for spec %q, got 0x%02x", spec, b)
		}

		result[spec] = content
	}
	return result, nil
}
