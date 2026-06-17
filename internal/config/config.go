// Package config persists per-customer workspace aliases and favourites
// to a JSON file on disk. Each customer has a list of Environments where
// an Environment is an alias (e.g. "DEV", "feature") paired with the
// Fabric workspace display name that alias resolves to.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DeployMapping ties a repo subfolder to the workspace its items deploy to,
// for one environment. Used only by the deploy flow; absent for customers
// that don't deploy. The folder is repo-relative (e.g. "Backend").
type DeployMapping struct {
	Folder    string `json:"folder"`
	Workspace string `json:"workspace"`
}

// ReferenceOverride maps a baseline-environment GUID baked in git to a target
// item resolved by name. Unlike parameter.yml's per-env blocks it is
// env-agnostic: the same entry resolves correctly for TEST and PROD because
// ItemName is looked up in whichever target env the deploy targets.
type ReferenceOverride struct {
	SourceGUID string `json:"source_guid"`
	ItemType   string `json:"item_type"`
	ItemName   string `json:"item_name"`
	Note       string `json:"note,omitempty"`
}

// Environment pairs a user-chosen alias (menu label) with one or more
// Fabric workspaces it resolves to. Multiple workspaces per alias is the
// common case for real Fabric deployments — e.g. a "DEV" environment
// often spans both a Config workspace (notebooks) and a SemMod
// workspace (semantic models). Run / Refresh aggregate items across
// every workspace under the chosen alias.
type Environment struct {
	Alias       string          `json:"alias"`
	Workspaces  []string        `json:"workspaces"`
	Deployments []DeployMapping `json:"deployments,omitempty"`
}

// Customer groups one tenant's environments and notebook favourites.
// A customer with zero Environments is valid — they just can't run
// notebooks until they add at least one via `futils edit`.
type Customer struct {
	Environments        []Environment       `json:"environments"`
	Favorites           []NotebookFavorite  `json:"favorites,omitempty"`
	RepoPath            string              `json:"repo_path,omitempty"`
	BaselineEnvironment string              `json:"baseline_environment,omitempty"`
	ReferenceOverrides  []ReferenceOverride `json:"reference_overrides,omitempty"`
}

// NotebookFavorite pins a single notebook (by displayName) and optionally
// a subset of its parameters. An empty Parameters slice means "no filter —
// show all parameters the Papermill cell declares".
type NotebookFavorite struct {
	Name       string   `json:"name"`
	Parameters []string `json:"parameters,omitempty"`
}

// AllWorkspaces returns every workspace this environment spans: the explicit
// Workspaces (which may include reference-only workspaces such as a Data
// workspace that is not a deploy target) unioned with every Deployment target,
// de-duplicated, preserving first-seen order. Used to build the env-wide name
// index for reference rebinding.
func (e Environment) AllWorkspaces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		if w == "" || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for _, w := range e.Workspaces {
		add(w)
	}
	for _, d := range e.Deployments {
		add(d.Workspace)
	}
	return out
}

// Workspaces returns the workspace display names mapped to an alias,
// plus a boolean indicating whether the alias was found. Callers that
// previously expected a single workspace should now iterate the slice
// — every flow that resolves items by env is expected to aggregate
// across all of them.
func (c Customer) Workspaces(alias string) ([]string, bool) {
	for _, e := range c.Environments {
		if e.Alias == alias {
			return e.Workspaces, true
		}
	}
	return nil, false
}

// DeployMappings returns the folder→workspace mappings for an alias, plus a
// bool indicating whether the alias was found. An env with no mappings returns
// (nil, true).
func (c Customer) DeployMappings(alias string) ([]DeployMapping, bool) {
	for _, e := range c.Environments {
		if e.Alias == alias {
			return e.Deployments, true
		}
	}
	return nil, false
}

// OverrideForGUID returns the reference override registered for a baseline
// GUID, and whether one was found.
func (c Customer) OverrideForGUID(guid string) (ReferenceOverride, bool) {
	for _, o := range c.ReferenceOverrides {
		if o.SourceGUID == guid {
			return o, true
		}
	}
	return ReferenceOverride{}, false
}

// EnvironmentByAlias returns the environment with the given alias, and whether
// it was found. Used to resolve the baseline environment's full workspace set.
func (c Customer) EnvironmentByAlias(alias string) (Environment, bool) {
	for _, e := range c.Environments {
		if e.Alias == alias {
			return e, true
		}
	}
	return Environment{}, false
}

// FavoriteFor returns the favourite entry for the given notebook name,
// and a boolean indicating whether it was found.
func (c Customer) FavoriteFor(name string) (NotebookFavorite, bool) {
	for _, f := range c.Favorites {
		if f.Name == name {
			return f, true
		}
	}
	return NotebookFavorite{}, false
}

// FavoriteNames returns the display names of favourited notebooks in
// saved order.
func (c Customer) FavoriteNames() []string {
	out := make([]string, len(c.Favorites))
	for i, f := range c.Favorites {
		out[i] = f.Name
	}
	return out
}

// UnmarshalJSON transparently migrates three legacy shapes onto the
// current `environments: [{alias, workspaces: []}]` form:
//
//  1. `workspace_pattern` + `environments: [string]` — earliest shape,
//     a pattern with {env} placeholder substituted per env alias.
//  2. `environments: [{alias, workspace_name: string}]` — intermediate
//     shape, single workspace per alias.
//  3. `environments: [{alias, workspaces: []string}]` — current shape.
//
// Detection peeks at the first env entry: a quote means string (#1),
// otherwise it's an object — and we look for `workspace_name` (#2) vs
// `workspaces` (#3) inside it. On next Save the file is rewritten in
// the current shape and legacy fields disappear.
func (c *Customer) UnmarshalJSON(data []byte) error {
	aux := struct {
		WorkspacePattern    string              `json:"workspace_pattern"`
		Environments        []json.RawMessage   `json:"environments"`
		Favorites           []NotebookFavorite  `json:"favorites,omitempty"`
		RepoPath            string              `json:"repo_path,omitempty"`
		BaselineEnvironment string              `json:"baseline_environment,omitempty"`
		ReferenceOverrides  []ReferenceOverride `json:"reference_overrides,omitempty"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	c.Favorites = aux.Favorites
	c.RepoPath = aux.RepoPath
	c.BaselineEnvironment = aux.BaselineEnvironment
	c.ReferenceOverrides = aux.ReferenceOverrides

	if len(aux.Environments) == 0 {
		c.Environments = nil
		return nil
	}

	first := aux.Environments[0]
	// Legacy shape #1: each env entry is a bare string.
	if len(first) > 0 && first[0] == '"' {
		for _, raw := range aux.Environments {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return fmt.Errorf("legacy environments entry: %w", err)
			}
			workspace := strings.ReplaceAll(aux.WorkspacePattern, "{env}", s)
			c.Environments = append(c.Environments, Environment{
				Alias:      s,
				Workspaces: []string{workspace},
			})
		}
		return nil
	}

	// Object shape — could be #2 (workspace_name) or #3 (workspaces).
	// Decode both fields; whichever is populated wins.
	for _, raw := range aux.Environments {
		var entry struct {
			Alias         string          `json:"alias"`
			WorkspaceName string          `json:"workspace_name"`
			Workspaces    []string        `json:"workspaces"`
			Deployments   []DeployMapping `json:"deployments"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("environments entry: %w", err)
		}
		env := Environment{Alias: entry.Alias, Workspaces: entry.Workspaces, Deployments: entry.Deployments}
		if len(env.Workspaces) == 0 && entry.WorkspaceName != "" {
			env.Workspaces = []string{entry.WorkspaceName}
		}
		c.Environments = append(c.Environments, env)
	}
	return nil
}

type Config struct {
	Customers map[string]Customer `json:"customers"`
}

// GetConfigPath returns the platform-appropriate config location.
func GetConfigPath() string {
	var base string
	if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
	} else {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "futils", "config.json")
}

func Load(path string) (Config, error) {
	cfg := Config{Customers: make(map[string]Customer)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Customers == nil {
		cfg.Customers = make(map[string]Customer)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: temp file in the same directory + rename. Avoids
	// leaving a half-written config.json behind if the process is killed
	// or the disk fills mid-write — which would wipe every customer /
	// favourite mapping on next Load.
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below fails before Rename.
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp config: %w", err)
	}
	return nil
}

func AddCustomer(path string, name string, customer Customer) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	cfg.Customers[name] = customer
	return Save(path, cfg)
}

func EditCustomer(path string, name string, customer Customer) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if _, ok := cfg.Customers[name]; !ok {
		return fmt.Errorf("customer '%s' not found in config", name)
	}
	cfg.Customers[name] = customer
	return Save(path, cfg)
}

func RemoveCustomer(path string, name string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if _, ok := cfg.Customers[name]; !ok {
		return fmt.Errorf("customer '%s' not found in config", name)
	}
	delete(cfg.Customers, name)
	return Save(path, cfg)
}

func SetFavorites(path string, name string, favorites []NotebookFavorite) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[name]
	if !ok {
		return fmt.Errorf("customer '%s' not found in config", name)
	}
	customer.Favorites = favorites
	cfg.Customers[name] = customer
	return Save(path, cfg)
}
