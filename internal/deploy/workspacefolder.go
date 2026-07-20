package deploy

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// WorkspaceFolderPath derives the target workspace folder an item should land
// in from its repo path and the mapping root it deploys under. The mapping
// root is stripped, then the item's own folder segment is dropped — what's
// left is the workspace folder path. Examples (root "Backend"):
//
//	Backend/X.Notebook                → ""            (workspace root)
//	Backend/Sub/X.Notebook            → "Sub"
//	Backend/Notebooks/Config/X.Notebook → "Notebooks/Config"
//
// An empty root (whole-repo mapping) treats the full path minus the item
// segment as the folder path. Slashes are normalized.
func WorkspaceFolderPath(itemFolderPath, mappingRoot string) string {
	fp := strings.Trim(itemFolderPath, "/")
	root := strings.Trim(mappingRoot, "/")
	if root != "" {
		fp = strings.TrimPrefix(fp, root+"/")
		if fp == strings.Trim(itemFolderPath, "/") {
			// itemFolderPath wasn't actually under root (shouldn't happen given
			// ItemsInFolder gating) — no reliable folder, place at root.
			return ""
		}
	}
	dir := path.Dir(fp)
	if dir == "." {
		return ""
	}
	return dir
}

// ensureWorkspaceFolders makes sure every workspace folder the plan's
// to-be-created items need exists in the target, returning folderPath →
// folderId (including "" → "" for the root). It lists existing folders once,
// then creates missing ones shallowest-first so each nested folder's parent
// id is known. Returns a non-nil error if any create fails — the caller keeps
// deploying (items in the failed folder land at root with a warning) rather
// than aborting. No folder-bearing new items → no API calls.
func ensureWorkspaceFolders(client FabricClient, token, workspaceID string, plan []PlannedItem) (map[string]string, error) {
	var desired []string
	for _, p := range plan {
		if p.Action != ActionUpdate && p.WorkspaceFolder != "" {
			desired = append(desired, p.WorkspaceFolder)
		}
	}
	byPath := map[string]string{"": ""} // root
	if len(desired) == 0 {
		return byPath, nil
	}

	existing, err := client.ListFolders(token, workspaceID)
	if err != nil {
		return byPath, fmt.Errorf("list folders: %w", err)
	}
	for p, id := range folderFullPaths(existing) {
		byPath[p] = id
	}

	for _, p := range neededFolderPaths(desired) {
		if _, ok := byPath[p]; ok {
			continue // already exists (or created earlier this pass)
		}
		parentPath := path.Dir(p)
		if parentPath == "." {
			parentPath = ""
		}
		parentID, ok := byPath[parentPath]
		if !ok {
			// Parent creation failed earlier — can't place this one either.
			return byPath, fmt.Errorf("parent folder %q missing for %q", parentPath, p)
		}
		f, err := client.CreateFolder(token, workspaceID, path.Base(p), parentID)
		if err != nil {
			return byPath, fmt.Errorf("create folder %q: %w", p, err)
		}
		byPath[p] = f.ID
	}
	return byPath, nil
}

// folderFullPaths reconstructs each existing workspace folder's full
// slash-joined path (e.g. "Notebooks/Config") from the flat folder list by
// walking parentFolderId chains, returning path → folderId. A folder whose
// parent chain can't be fully resolved (orphaned parent) is skipped rather
// than misplaced.
func folderFullPaths(folders []fabric.Folder) map[string]string {
	byID := make(map[string]fabric.Folder, len(folders))
	for _, f := range folders {
		byID[f.ID] = f
	}
	out := make(map[string]string, len(folders))
	for _, f := range folders {
		segs := []string{f.DisplayName}
		cur := f
		ok := true
		visited := map[string]bool{f.ID: true}
		for cur.ParentFolderID != "" {
			// A parentFolderId cycle in the listing (shouldn't happen, but the
			// data is remote) would otherwise walk forever.
			if visited[cur.ParentFolderID] {
				ok = false
				break
			}
			visited[cur.ParentFolderID] = true
			parent, found := byID[cur.ParentFolderID]
			if !found {
				ok = false
				break
			}
			segs = append([]string{parent.DisplayName}, segs...)
			cur = parent
		}
		if ok {
			out[strings.Join(segs, "/")] = f.ID
		}
	}
	return out
}

// neededFolderPaths returns every distinct folder path (and its ancestors) the
// given desired leaf paths require, sorted shallowest-first so a create pass
// can build parents before children. "" (root) and duplicates are dropped.
func neededFolderPaths(desired []string) []string {
	seen := map[string]bool{}
	for _, d := range desired {
		d = strings.Trim(d, "/")
		for d != "" && d != "." {
			seen[d] = true
			d = path.Dir(d)
			if d == "." {
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	// Shallowest-first: fewer separators sorts before more; ties alphabetical
	// for determinism.
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i], "/"), strings.Count(out[j], "/")
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}
