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
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		fp := strings.Trim(it.FolderPath, "/")
		i := strings.Index(fp, "/")
		if i < 0 {
			continue // item at repo root — no grouping folder
		}
		seg := fp[:i]
		if !seen[seg] {
			seen[seg] = true
			out = append(out, seg)
		}
	}
	sort.Strings(out)
	return out
}
