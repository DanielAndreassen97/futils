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
