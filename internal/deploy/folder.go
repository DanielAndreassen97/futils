package deploy

import "strings"

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
