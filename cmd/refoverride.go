package cmd

import (
	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// RefAction kind constants — used as MenuOption Values and matched in dispatch
// switches so a rename is compiler-checked rather than grep-dependent.
const (
	refActionOverride = "override"
	refActionRegister = "register"
	refActionIgnore   = "ignore"
	refActionSkip     = "skip"
)

// refActionOptions builds the action menu for one unresolved reference, ordered
// by its Reason: when the item is likely just in an unregistered workspace
// (not-in-target / ambiguous), lead with "register"; for name-unknown lead with
// "override" since we have no name to search by. All actions remain available.
func refActionOptions(ref deploy.UnresolvedRef) []ui.MenuOption {
	register := ui.MenuOption{Label: "Register the workspace it lives in (resolve by name)", Value: refActionRegister}
	override := ui.MenuOption{Label: "Map it to a specific item (pick workspace → item)", Value: refActionOverride}
	ignore := ui.MenuOption{Label: "Ignore (leave as-is, don't ask again)", Value: refActionIgnore}
	skip := ui.MenuOption{Label: "Skip for now", Value: refActionSkip}
	switch ref.Reason {
	case deploy.ReasonNotInTarget, deploy.ReasonAmbiguous:
		return []ui.MenuOption{register, override, ignore, skip}
	default: // ReasonNameUnknown or unset
		return []ui.MenuOption{override, register, ignore, skip}
	}
}

// RefAction is one user choice for resolving an unresolved reference.
type RefAction struct {
	Kind      string // refActionOverride | refActionIgnore | refActionRegister | refActionSkip
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
	case refActionOverride:
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
	case refActionIgnore:
		if !c.IsIgnored(ref.GUID) {
			c.IgnoredReferences = append(c.IgnoredReferences, ref.GUID)
		}
	case refActionRegister:
		next := make([]config.Environment, len(c.Environments))
		copy(next, c.Environments)
		for i := range next {
			if next[i].Alias != a.EnvAlias {
				continue
			}
			for _, w := range next[i].Workspaces {
				if w == a.Workspace {
					c.Environments = next
					return c // already registered
				}
			}
			ws := append([]string{}, next[i].Workspaces...) // fresh Workspaces copy — no aliasing
			next[i].Workspaces = append(ws, a.Workspace)
		}
		c.Environments = next
	}
	return c
}
