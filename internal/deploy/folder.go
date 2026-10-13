package deploy

import (
	"sort"
	"strings"
)

// ItemsInFolder returns the items whose FolderPath is inside the given repo
// subfolder. An empty folder matches everything (whole-repo fallback). Slashes
// are normalized so "Backend", "/Backend", and "Backend/" behave identically.
// Matching is prefix-on-a-path-boundary: "Backend" matches "Backend/x" but not
// "BackendExtra/x".
func ItemsInFolder(items []LocalItem, folder string) []LocalItem {
	folder = strings.Trim(folder, "/")
	if folder == "" {
		return items
	}
	prefix := folder + "/"
	var out []LocalItem
	for _, it := range items {
		fp := strings.Trim(it.FolderPath, "/")
		if fp == folder || strings.HasPrefix(fp, prefix) {
			out = append(out, it)
		}
	}
	return out
}

// TopLevelFolders returns the distinct first path segments of the items'
// FolderPaths (e.g. "FabricBackEnd" from "FabricBackEnd/NB_X.Notebook"), sorted.
// Items sitting directly at the repo root (no separator in FolderPath) have no
// grouping folder and are skipped. Used to offer a pick-list of mappable
// folders during deploy setup.
func TopLevelFolders(items []LocalItem) []string {
	subs, _ := SubFolders(items, "")
	return subs
}

// SubFolders returns the immediate child folders of parent that hold items at
// any depth, as full repo paths ("FrontEnd/Reports"), sorted, plus how many
// items sit directly in parent itself. An empty parent means the repo root.
//
// A FolderPath ends in the item's own directory ("FrontEnd/X.Report"), so an
// item is "direct" when nothing but that directory follows parent. Those items
// are what a mapping per subfolder would leave uncovered, which is why the
// count comes back alongside the folders.
func SubFolders(items []LocalItem, parent string) (subs []string, direct int) {
	parent = strings.Trim(parent, "/")
	prefix := ""
	if parent != "" {
		prefix = parent + "/"
	}
	seen := map[string]bool{}
	for _, it := range items {
		fp := strings.Trim(it.FolderPath, "/")
		if !strings.HasPrefix(fp, prefix) {
			continue
		}
		rest := fp[len(prefix):]
		i := strings.Index(rest, "/")
		if i < 0 {
			direct++
			continue
		}
		sub := prefix + rest[:i]
		if !seen[sub] {
			seen[sub] = true
			subs = append(subs, sub)
		}
	}
	sort.Strings(subs)
	return subs, direct
}
