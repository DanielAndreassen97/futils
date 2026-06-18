package cmd

import (
	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
)

// RefAction is one user choice for resolving an unresolved reference.
type RefAction struct {
	Kind      string // "override" | "ignore" | "register" | "skip"
	ItemType  string // override: the target item's type
	ItemName  string // override: the target item's name (resolved per env)
	EnvAlias  string // register: which environment to add the workspace to
	Workspace string // register: the reference-only workspace to add
}

// applyRefAction returns the customer with the action applied. Override appends
// or replaces a ReferenceOverride keyed on the baseline GUID; ignore adds the
// GUID to the ignore list (idempotent); register adds a reference-only
// workspace to an environment (idempotent); skip is a no-op. Pure — no I/O.
func applyRefAction(c config.Customer, ref deploy.UnresolvedRef, a RefAction) config.Customer {
	switch a.Kind {
	case "override":
		next := make([]config.ReferenceOverride, 0, len(c.ReferenceOverrides)+1)
		for _, o := range c.ReferenceOverrides {
			if o.SourceGUID != ref.GUID {
				next = append(next, o)
			}
		}
		next = append(next, config.ReferenceOverride{
			SourceGUID: ref.GUID, ItemType: a.ItemType, ItemName: a.ItemName,
		})
		c.ReferenceOverrides = next
	case "ignore":
		if !c.IsIgnored(ref.GUID) {
			c.IgnoredReferences = append(c.IgnoredReferences, ref.GUID)
		}
	case "register":
		for i := range c.Environments {
			if c.Environments[i].Alias != a.EnvAlias {
				continue
			}
			for _, w := range c.Environments[i].Workspaces {
				if w == a.Workspace {
					return c // already registered
				}
			}
			c.Environments[i].Workspaces = append(c.Environments[i].Workspaces, a.Workspace)
		}
	}
	return c
}
