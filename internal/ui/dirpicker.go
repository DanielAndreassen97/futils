package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Sentinel Values for the two non-directory actions in the picker. NUL prefix
// can't collide with a real folder name.
const (
	dirSelectValue = "\x00select"
	dirUpValue     = "\x00up"
)

// isGitRepo reports whether dir contains a .git entry. A worktree's .git is a
// file rather than a directory, so we stat without checking the mode.
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// gitRepoRoot walks up from dir to find the nearest ancestor (inclusive) that
// is a git repo, returning its path and true. Returns ("", false) if dir is not
// inside any git repo.
func gitRepoRoot(dir string) (string, bool) {
	d := dir
	for {
		if isGitRepo(d) {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d { // reached filesystem root
			return "", false
		}
		d = parent
	}
}

// dirOptions builds the FilterMenu rows for browsing cur: a "use this folder"
// action (annotated with whether cur is a git repo), an "up one level" action
// (omitted at the filesystem root), then one row per visible subdirectory.
// Hidden dirs (dot-prefixed) are skipped to cut noise; git repos are tagged via
// Meta so the renderer can mark them.
func dirOptions(cur string) ([]FilterOption, error) {
	entries, err := os.ReadDir(cur)
	if err != nil {
		return nil, err
	}
	var subdirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			subdirs = append(subdirs, e.Name())
		}
	}
	sort.Strings(subdirs)

	selLabel := "✓ Use this folder (not a git repo)"
	if root, ok := gitRepoRoot(cur); ok {
		if root == cur {
			selLabel = "✓ Use this folder (git repo)"
		} else {
			selLabel = fmt.Sprintf("✓ Use this folder (inside repo %s → uses repo root)", filepath.Base(root))
		}
	}
	opts := []FilterOption{{Label: selLabel, Value: dirSelectValue, Meta: "action"}}
	if cur != filepath.Dir(cur) {
		opts = append(opts, FilterOption{Label: "⬆ .. (up one level)", Value: dirUpValue, Meta: "action"})
	}
	for _, name := range subdirs {
		meta := ""
		if isGitRepo(filepath.Join(cur, name)) {
			meta = "git"
		}
		opts = append(opts, FilterOption{Label: name, Value: name, Meta: meta})
	}
	return opts, nil
}

// nextDir resolves a FilterMenu choice into the next directory and whether the
// selection is final.
func nextDir(cur, choice string) (next string, done bool) {
	switch choice {
	case dirSelectValue:
		return cur, true
	case dirUpValue:
		return filepath.Dir(cur), false
	default:
		return filepath.Join(cur, choice), false
	}
}

// dirRowRenderer marks git repos with a ● and dims the action rows. Selection
// takes precedence with a uniform accent highlight (FilterMenu contract).
func dirRowRenderer(opt FilterOption, selected bool) string {
	if selected {
		return lipgloss.NewStyle().Foreground(AccentColor).Bold(true).Render(opt.Label)
	}
	switch opt.Meta {
	case "git":
		return lipgloss.NewStyle().Foreground(AccentColor).Render("● ") + opt.Label
	case "action":
		return lipgloss.NewStyle().Foreground(DimColor).Render(opt.Label)
	default:
		return "  " + opt.Label
	}
}

// PickDirectory shows a searchable, navigable directory browser rooted at
// startDir and returns the absolute path the user selects. Type to filter the
// current folder's contents; Enter on a subfolder descends into it; choose
// "Use this folder" to pick the current directory. Git repos are marked with a
// ● . Returns ErrGoBack on esc, ErrQuit on ctrl+c.
func PickDirectory(title, startDir string) (string, error) {
	cur := startDir
	if cur == "" {
		var err error
		cur, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine a start directory: %w", err)
		}
	}
	for {
		opts, err := dirOptions(cur)
		if err != nil {
			return "", err
		}
		choice, err := FilterMenu(fmt.Sprintf("%s — %s", title, cur), opts, dirRowRenderer)
		if err != nil {
			return "", err // ErrGoBack / ErrQuit propagate
		}
		next, done := nextDir(cur, choice)
		if done {
			if root, ok := gitRepoRoot(next); ok {
				return root, nil
			}
			return next, nil
		}
		cur = next
	}
}
