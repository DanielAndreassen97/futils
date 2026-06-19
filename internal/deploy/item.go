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
	if repoPath == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(repoPath, func(p string, d fs.DirEntry, err error) error {
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
		meta, perr := parsePlatform(raw)
		if perr == nil && meta.Type != "" {
			seen[meta.Type] = true
		}
		return nil // skip unparseable .platform, don't fail the whole scan
	})
	if err != nil {
		return nil, fmt.Errorf("scan repo item types: %w", err)
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types, nil
}
