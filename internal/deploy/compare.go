package deploy

import "github.com/DanielAndreassen97/futils/internal/fabric"

type Class int

const (
	ClassNew       Class = iota // local item not present in target
	ClassExists                 // local item present in target (pre-content-diff / unverified)
	ClassOrphan                 // target item with no local counterpart
	ClassChanged                // exists in target and content differs
	ClassUnchanged              // exists in target and content matches
)

func (c Class) String() string {
	switch c {
	case ClassNew:
		return "New"
	case ClassExists:
		return "Exists"
	case ClassOrphan:
		return "Orphan"
	case ClassChanged:
		return "Changed"
	case ClassUnchanged:
		return "Unchanged"
	}
	return "?"
}

// CompareRow is one line in the compare view. Local is set for New/Exists;
// Deployed is set for Exists/Orphan.
type CompareRow struct {
	Class      Class
	Local      LocalItem
	Deployed   fabric.Item
	DeployedID string
}

// Name returns the displayName regardless of which side populated the row.
func (r CompareRow) Name() string {
	if r.Class == ClassOrphan {
		return r.Deployed.DisplayName
	}
	return r.Local.DisplayName
}

// ItemType returns the type regardless of side.
func (r CompareRow) ItemType() string {
	if r.Class == ClassOrphan {
		return r.Deployed.Type
	}
	return r.Local.Type
}

// key uniquely identifies an item by type+name (fabric-cicd's identity).
func key(itemType, name string) string { return itemType + "\x00" + name }

// Compare classifies local items against the deployed set by displayName+type.
// scope limits which deployed types can be flagged as orphans (so a workspace
// full of out-of-scope items isn't reported as orphaned).
func Compare(local []LocalItem, deployed []fabric.Item, scope map[string]bool) []CompareRow {
	deployedByKey := make(map[string]fabric.Item, len(deployed))
	for _, d := range deployed {
		deployedByKey[key(d.Type, d.DisplayName)] = d
	}
	localKeys := make(map[string]bool, len(local))

	var rows []CompareRow
	for _, l := range local {
		k := key(l.Type, l.DisplayName)
		localKeys[k] = true
		if d, ok := deployedByKey[k]; ok {
			rows = append(rows, CompareRow{Class: ClassExists, Local: l, Deployed: d, DeployedID: d.ID})
		} else {
			rows = append(rows, CompareRow{Class: ClassNew, Local: l})
		}
	}
	for _, d := range deployed {
		if !scope[d.Type] {
			continue
		}
		if !localKeys[key(d.Type, d.DisplayName)] {
			rows = append(rows, CompareRow{Class: ClassOrphan, Deployed: d, DeployedID: d.ID})
		}
	}
	return rows
}
