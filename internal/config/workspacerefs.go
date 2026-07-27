package config

import "sort"

// A workspace name appears in exactly three places in the config:
//
//   - Environment.Workspaces         — the env's workspace list
//   - DeployMapping.Workspace        — a folder's deploy target
//   - DeployMapping.BaselineWorkspace — a mapping's baseline override
//
// Reference overrides and custom substitutions resolve *items* by name, never
// workspaces, so they are deliberately out of scope. Everything here is pure:
// no file I/O, no API calls, so the caller controls when (and whether) the
// change is persisted.

// WorkspaceRefKind says which of the three fields a reference came from.
type WorkspaceRefKind int

const (
	RefEnvWorkspace WorkspaceRefKind = iota
	RefDeployTarget
	RefDeployBaseline
)

func (k WorkspaceRefKind) String() string {
	switch k {
	case RefEnvWorkspace:
		return "workspace"
	case RefDeployTarget:
		return "deploy mapping"
	case RefDeployBaseline:
		return "baseline workspace"
	}
	return "unknown"
}

// WorkspaceRef is one place in the config that names a workspace. Mapping holds
// the deploy mapping's folder for the two deploy kinds and is empty otherwise.
type WorkspaceRef struct {
	Customer    string
	Environment string
	Kind        WorkspaceRefKind
	Mapping     string
}

// FindWorkspaceRefs returns every config reference to the named workspace,
// sorted by customer then environment then kind. Matching is exact and
// case-sensitive, mirroring how authAndResolveWorkspaces resolves a name — a
// looser match here would promise to repair references that never resolved.
//
// The sort matters: Config.Customers is a map, so an unsorted scan would
// reshuffle the impact list the user is asked to confirm.
func FindWorkspaceRefs(cfg Config, name string) []WorkspaceRef {
	var out []WorkspaceRef
	for _, customer := range sortedCustomerNames(cfg) {
		for _, env := range cfg.Customers[customer].Environments {
			for _, ws := range env.Workspaces {
				if ws == name {
					out = append(out, WorkspaceRef{Customer: customer, Environment: env.Alias, Kind: RefEnvWorkspace})
				}
			}
			for _, dep := range env.Deployments {
				if dep.Workspace == name {
					out = append(out, WorkspaceRef{Customer: customer, Environment: env.Alias, Kind: RefDeployTarget, Mapping: dep.Folder})
				}
				if dep.BaselineWorkspace == name {
					out = append(out, WorkspaceRef{Customer: customer, Environment: env.Alias, Kind: RefDeployBaseline, Mapping: dep.Folder})
				}
			}
		}
	}
	return out
}

// RenameWorkspaceRefs rewrites every reference to oldName as newName and
// returns how many it changed. Call it only after Fabric has confirmed the
// rename: config must never claim a name Fabric does not have.
func RenameWorkspaceRefs(cfg *Config, oldName, newName string) int {
	changed := 0
	forEachCustomer(cfg, func(c *Customer) {
		for ei := range c.Environments {
			env := &c.Environments[ei]
			for wi, ws := range env.Workspaces {
				if ws == oldName {
					env.Workspaces[wi] = newName
					changed++
				}
			}
			for di := range env.Deployments {
				dep := &env.Deployments[di]
				if dep.Workspace == oldName {
					dep.Workspace = newName
					changed++
				}
				if dep.BaselineWorkspace == oldName {
					dep.BaselineWorkspace = newName
					changed++
				}
			}
		}
	})
	return changed
}

// RemoveWorkspaceRefs drops every reference to the named workspace and returns
// how many it removed. The three kinds are removed differently on purpose:
//
//   - an environment simply loses the name, and an environment left with zero
//     workspaces is kept — that is a valid config state, and dropping it would
//     throw away its alias and deploy mappings too;
//   - a deploy mapping whose target is gone is removed, since a mapping without
//     a target cannot deploy anywhere;
//   - a baseline override is cleared, leaving the mapping to fall back to the
//     customer-level baseline environment.
func RemoveWorkspaceRefs(cfg *Config, name string) int {
	removed := 0
	forEachCustomer(cfg, func(c *Customer) {
		for ei := range c.Environments {
			env := &c.Environments[ei]

			var n int
			env.Workspaces, n = removeMatching(env.Workspaces, func(ws string) bool { return ws == name })
			removed += n

			// Baselines are cleared in place first, so a mapping whose baseline is
			// gone survives and falls back to the customer-level one instead of
			// being thrown away with it.
			for di := range env.Deployments {
				if env.Deployments[di].BaselineWorkspace == name {
					env.Deployments[di].BaselineWorkspace = ""
					removed++
				}
			}
			env.Deployments, n = removeMatching(env.Deployments,
				func(d DeployMapping) bool { return d.Workspace == name })
			removed += n
		}
	})
	return removed
}

// forEachCustomer hands each customer to fn by pointer and writes the mutated
// value back. Customers live in a map, so the value copy must be reassigned —
// mutating the range variable alone is a no-op.
func forEachCustomer(cfg *Config, fn func(*Customer)) {
	for _, name := range sortedCustomerNames(*cfg) {
		customer := cfg.Customers[name]
		fn(&customer)
		cfg.Customers[name] = customer
	}
}

// removeMatching drops every element match reports on, returning the kept slice
// and how many went. Six copies of this loop existed across the two ref files,
// differing only in the predicate.
func removeMatching[T any](in []T, match func(T) bool) ([]T, int) {
	kept := in[:0]
	removed := 0
	for _, v := range in {
		if match(v) {
			removed++
			continue
		}
		kept = append(kept, v)
	}
	return kept, removed
}

func sortedCustomerNames(cfg Config) []string {
	names := make([]string, 0, len(cfg.Customers))
	for name := range cfg.Customers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
