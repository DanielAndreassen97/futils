package config

// An item display name appears in five places in the config:
//
//   - Favorites[].Name              — a pinned notebook
//   - PostDeployRuns[]              — offered for execution after a deploy
//   - ReferenceOverrides[].ItemName — the item a baked GUID resolves to
//   - Substitutions[].TargetName    — the item a find→replace resolves against
//   - Substitutions[].ItemName      — an optional filter narrowing a rule
//
// Unlike workspace references, these are NOT rewritten automatically when an
// item is renamed. They are environment-agnostic by design: an override names
// an item that is looked up in whichever environment a deploy targets, and the
// same notebook name exists in DEV, TEST and PROD. Renaming one copy and
// silently rewriting config would make the entry right for that environment and
// wrong for every other. The caller lists what FindItemRefs returns and asks.
//
// Everything here is pure: no file I/O, no API calls.

// ItemRefKind says which of the five fields a reference came from.
type ItemRefKind int

const (
	RefFavorite ItemRefKind = iota
	RefPostDeployRun
	RefOverrideTarget
	RefSubstitutionTarget
	RefSubstitutionFilter
)

func (k ItemRefKind) String() string {
	switch k {
	case RefFavorite:
		return "favourite"
	case RefPostDeployRun:
		return "post-deploy run"
	case RefOverrideTarget:
		return "reference override"
	case RefSubstitutionTarget:
		return "substitution target"
	case RefSubstitutionFilter:
		return "substitution filter"
	}
	return "unknown"
}

// ItemRef is one place in the config that names an item. Detail carries the
// entry's own identifier — a substitution's find value, or an override's source
// GUID — so a list of references can distinguish two rules on the same item.
type ItemRef struct {
	Customer string
	Kind     ItemRefKind
	Detail   string
}

// FindItemRefs returns every config reference to the named item, sorted by
// customer then by the field order above. Matching is exact and case-sensitive,
// as everywhere else futils resolves an item by name.
func FindItemRefs(cfg Config, name string) []ItemRef {
	var out []ItemRef
	for _, customer := range sortedCustomerNames(cfg) {
		c := cfg.Customers[customer]
		for _, fav := range c.Favorites {
			if fav.Name == name {
				out = append(out, ItemRef{Customer: customer, Kind: RefFavorite})
			}
		}
		for _, run := range c.PostDeployRuns {
			if run == name {
				out = append(out, ItemRef{Customer: customer, Kind: RefPostDeployRun})
			}
		}
		for _, ov := range c.ReferenceOverrides {
			if ov.ItemName == name {
				out = append(out, ItemRef{Customer: customer, Kind: RefOverrideTarget, Detail: ov.SourceGUID})
			}
		}
		for _, sub := range c.Substitutions {
			if sub.TargetName == name {
				out = append(out, ItemRef{Customer: customer, Kind: RefSubstitutionTarget, Detail: sub.FindValue})
			}
			if sub.ItemName == name {
				out = append(out, ItemRef{Customer: customer, Kind: RefSubstitutionFilter, Detail: sub.FindValue})
			}
		}
	}
	return out
}

// RenameItemRefs rewrites every reference to oldName as newName and returns how
// many it changed. Only ever called after the user has explicitly accepted the
// rewrite — see the package comment for why it is not automatic.
func RenameItemRefs(cfg *Config, oldName, newName string) int {
	changed := 0
	forEachCustomer(cfg, func(c *Customer) {
		for i := range c.Favorites {
			if c.Favorites[i].Name == oldName {
				c.Favorites[i].Name = newName
				changed++
			}
		}
		for i := range c.PostDeployRuns {
			if c.PostDeployRuns[i] == oldName {
				c.PostDeployRuns[i] = newName
				changed++
			}
		}
		for i := range c.ReferenceOverrides {
			if c.ReferenceOverrides[i].ItemName == oldName {
				c.ReferenceOverrides[i].ItemName = newName
				changed++
			}
		}
		for i := range c.Substitutions {
			if c.Substitutions[i].TargetName == oldName {
				c.Substitutions[i].TargetName = newName
				changed++
			}
			if c.Substitutions[i].ItemName == oldName {
				c.Substitutions[i].ItemName = newName
				changed++
			}
		}
	})
	return changed
}

// RemoveItemRefs drops every reference to the named item and returns how many
// it removed. A substitution is treated in two different ways on purpose: one
// whose TARGET is gone can no longer resolve and is removed, while one that
// merely FILTERS on the name still works without the filter, so the filter is
// cleared and the rule survives — wider than before, but not silently deleted.
func RemoveItemRefs(cfg *Config, name string) int {
	removed := 0
	forEachCustomer(cfg, func(c *Customer) {
		keptFavs := c.Favorites[:0]
		for _, fav := range c.Favorites {
			if fav.Name == name {
				removed++
				continue
			}
			keptFavs = append(keptFavs, fav)
		}
		c.Favorites = keptFavs

		keptRuns := c.PostDeployRuns[:0]
		for _, run := range c.PostDeployRuns {
			if run == name {
				removed++
				continue
			}
			keptRuns = append(keptRuns, run)
		}
		c.PostDeployRuns = keptRuns

		keptOverrides := c.ReferenceOverrides[:0]
		for _, ov := range c.ReferenceOverrides {
			if ov.ItemName == name {
				removed++
				continue
			}
			keptOverrides = append(keptOverrides, ov)
		}
		c.ReferenceOverrides = keptOverrides

		keptSubs := c.Substitutions[:0]
		for _, sub := range c.Substitutions {
			if sub.TargetName == name {
				removed++
				continue
			}
			if sub.ItemName == name {
				sub.ItemName = ""
				removed++
			}
			keptSubs = append(keptSubs, sub)
		}
		c.Substitutions = keptSubs
	})
	return removed
}
