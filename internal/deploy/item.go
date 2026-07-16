// Package deploy implements generic Fabric item deployment: it reads items
// from a repo's origin/main, applies parameter.yml substitution, compares
// against a target workspace, and publishes in dependency order. It mirrors
// the core of Microsoft's fabric-cicd, fronted by the interactive TUI in
// package cmd. All Fabric access goes through the narrow FabricClient
// interface so the logic is unit-testable with a fake.
package deploy

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type Part struct {
	Path    string
	Content []byte
}

type LocalItem struct {
	Type        string
	DisplayName string
	LogicalID   string
	Description string
	FolderPath  string
	Parts       []Part
	Platform    []byte // raw .platform bytes; retained for the bulk backend (not a Part)
}

// platformMeta is the subset of .platform we consume.
type platformMeta struct {
	Type        string
	DisplayName string
	Description string
	LogicalID   string
}

// parsePlatform reads a Fabric .platform file. Type and displayName are
// required (an item folder without them isn't deployable); logicalId is
// required by Fabric but we tolerate its absence here and let publish-time
// validation surface it, since some hand-authored items omit it.
func parsePlatform(raw []byte) (platformMeta, error) {
	var p struct {
		Metadata struct {
			Type        string `json:"type"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
		} `json:"metadata"`
		Config struct {
			LogicalID string `json:"logicalId"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return platformMeta{}, fmt.Errorf("parse .platform: %w", err)
	}
	if p.Metadata.Type == "" {
		return platformMeta{}, fmt.Errorf(".platform missing metadata.type")
	}
	if p.Metadata.DisplayName == "" {
		return platformMeta{}, fmt.Errorf(".platform missing metadata.displayName")
	}
	return platformMeta{
		Type:        p.Metadata.Type,
		DisplayName: p.Metadata.DisplayName,
		Description: p.Metadata.Description,
		LogicalID:   p.Config.LogicalID,
	}, nil
}

// RepoItemTypes scans a repo directory for Fabric item folders (those containing
// a .platform file) and returns the distinct item types found, sorted. Used by
// the edit-customer "exclude item types" picker — a cheap local scan, no API.
// A missing or empty repoPath yields an empty slice, not an error.
func RepoItemTypes(repoPath string) ([]string, error) {
	seen := map[string]bool{}
	if err := walkPlatforms(repoPath, func(meta platformMeta) {
		if meta.Type != "" {
			seen[meta.Type] = true
		}
	}); err != nil {
		return nil, fmt.Errorf("scan repo item types: %w", err)
	}
	return sortedKeys(seen), nil
}

// RepoItemNames scans repoPath (the working tree) for Fabric items of the
// given type and returns their display names, sorted and de-duplicated.
// Mirrors RepoItemTypes: unparseable .platform files are skipped, a missing
// path yields an empty result rather than an error.
func RepoItemNames(repoPath, itemType string) ([]string, error) {
	seen := map[string]bool{}
	if err := walkPlatforms(repoPath, func(meta platformMeta) {
		if meta.Type == itemType && meta.DisplayName != "" {
			seen[meta.DisplayName] = true
		}
	}); err != nil {
		return nil, fmt.Errorf("scan repo item names: %w", err)
	}
	return sortedKeys(seen), nil
}

// RepoItemTypesMulti unions RepoItemTypes across several repos (empty paths and
// missing paths skipped), sorted and de-duplicated.
func RepoItemTypesMulti(repoPaths []string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range repoPaths {
		if err := walkPlatforms(p, func(m platformMeta) {
			if m.Type != "" {
				seen[m.Type] = true
			}
		}); err != nil {
			return nil, fmt.Errorf("scan repo item types: %w", err)
		}
	}
	return sortedKeys(seen), nil
}

// RepoItemNamesMulti unions RepoItemNames of one type across several repos.
func RepoItemNamesMulti(repoPaths []string, itemType string) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range repoPaths {
		if err := walkPlatforms(p, func(m platformMeta) {
			if m.Type == itemType && m.DisplayName != "" {
				seen[m.DisplayName] = true
			}
		}); err != nil {
			return nil, fmt.Errorf("scan repo item names: %w", err)
		}
	}
	return sortedKeys(seen), nil
}

// walkPlatforms visits every parseable .platform file under repoPath, calling
// visit with its metadata. It owns the shared scan semantics: an empty repoPath
// is a no-op, a missing path is swallowed (not an error), and unparseable
// .platform files are skipped rather than failing the whole scan.
func walkPlatforms(repoPath string, visit func(meta platformMeta)) error {
	if repoPath == "" {
		return nil
	}
	return filepath.WalkDir(repoPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != ".platform" {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if meta, perr := parsePlatform(raw); perr == nil {
			visit(meta)
		}
		return nil // skip unparseable .platform, don't fail the whole scan
	})
}

// sortedKeys returns the map's keys as a sorted slice, or nil when empty (so
// an empty/missing repo scan yields nil, matching the callers' contract).
func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StripScheduleParts returns items with any ".schedules" definition part
// removed. Schedules are optional in the definition APIs — omitting the part
// on update leaves the target item's schedules untouched — so filtering here
// lets schedules be managed per environment instead of overwritten from git.
func StripScheduleParts(items []LocalItem) []LocalItem {
	out := make([]LocalItem, len(items))
	for i, it := range items {
		kept := it.Parts[:0:0]
		for _, p := range it.Parts {
			if filepath.Base(p.Path) == ".schedules" {
				continue
			}
			kept = append(kept, p)
		}
		it.Parts = kept
		out[i] = it
	}
	return out
}
