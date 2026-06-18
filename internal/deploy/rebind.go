package deploy

import (
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Override is a futils-native reference override resolved by name, keyed by the
// baseline GUID as it appears in git. It mirrors config.ReferenceOverride
// without the deploy package depending on config — the cmd layer converts.
type Override struct {
	ItemType string
	ItemName string
}

// UnresolvedRef is a baseline GUID the rebinder could not translate. Surfaced
// to the user so they can register an override (or ignore/strip it). ItemName
// is the notebook the GUID was found in, filled in by the substitution pass.
type UnresolvedRef struct {
	GUID     string
	ItemType string
	Location string // "default_lakehouse" | "known_lakehouses"
	ItemName string
}

// Rebinder translates baseline-environment GUIDs to a target env by item name.
type Rebinder struct {
	baseline  *NameIndex
	target    *NameIndex
	overrides map[string]Override // baseline GUID -> override
}

// NewRebinder builds the baseline and target name indices and returns a
// Rebinder. baselineWS / targetWS are each env's full workspace set (deploy
// targets plus reference-only workspaces). A nil overrides map is treated as
// empty.
func NewRebinder(client FabricClient, token string, baselineWS, targetWS []fabric.Workspace, overrides map[string]Override) (*Rebinder, error) {
	b, err := BuildNameIndex(client, token, baselineWS)
	if err != nil {
		return nil, err
	}
	t, err := BuildNameIndex(client, token, targetWS)
	if err != nil {
		return nil, err
	}
	if overrides == nil {
		overrides = map[string]Override{}
	}
	return &Rebinder{baseline: b, target: t, overrides: overrides}, nil
}

// resolveGUID translates one baseline GUID to its target item. An override
// (highest precedence) resolves its ItemName/ItemType directly in the target;
// otherwise the baseline index supplies the name and the target index supplies
// the new GUID. Returns false when it cannot be resolved.
func (rb *Rebinder) resolveGUID(guid string) (IndexedItem, bool) {
	if ov, ok := rb.overrides[guid]; ok {
		return rb.target.ItemByName(ov.ItemName, ov.ItemType)
	}
	base, ok := rb.baseline.ItemByGUID(guid)
	if !ok {
		return IndexedItem{}, false
	}
	return rb.target.ItemByName(base.Name, base.Type)
}

// RebindNotebookLakehouses rewrites the lakehouse dependency GUIDs in a Fabric
// notebook part from baseline to target, by name. It only touches GUIDs found
// in the dependencies.lakehouse metadata block (never arbitrary UUIDs in code),
// then string-replaces those exact GUIDs throughout the content (GUIDs are
// globally unique, so this affects only the metadata occurrences). GUIDs it
// cannot resolve are returned as UnresolvedRef and left unchanged. Content with
// no lakehouse block is returned unchanged.
func (rb *Rebinder) RebindNotebookLakehouses(content []byte) ([]byte, []UnresolvedRef) {
	lh, ok := parseNotebookLakehouse(content)
	if !ok {
		return content, nil
	}
	replacements := map[string]string{} // baseline GUID -> target GUID
	var unresolved []UnresolvedRef

	// default_lakehouse: prefer the stored name (zero-config, no baseline needed);
	// fall back to the override/baseline GUID path when the name is absent.
	if lh.DefaultLakehouse != "" {
		var it IndexedItem
		var resolved bool
		if _, hasOverride := rb.overrides[lh.DefaultLakehouse]; !hasOverride && lh.DefaultLakehouseName != "" {
			it, resolved = rb.target.ItemByName(lh.DefaultLakehouseName, "Lakehouse")
		} else {
			it, resolved = rb.resolveGUID(lh.DefaultLakehouse)
		}
		if resolved {
			replacements[lh.DefaultLakehouse] = it.GUID
			if lh.DefaultLakehouseWorkspaceID != "" && it.WorkspaceID != "" {
				replacements[lh.DefaultLakehouseWorkspaceID] = it.WorkspaceID
			}
		} else {
			unresolved = append(unresolved, UnresolvedRef{GUID: lh.DefaultLakehouse, ItemType: "Lakehouse", Location: "default_lakehouse"})
		}
	}

	// known_lakehouses: cosmetic; resolve by override, else baseline->target name.
	for _, k := range lh.KnownLakehouses {
		if k.ID == "" {
			continue
		}
		if _, done := replacements[k.ID]; done {
			continue
		}
		if it, ok := rb.resolveGUID(k.ID); ok {
			replacements[k.ID] = it.GUID
		} else {
			unresolved = append(unresolved, UnresolvedRef{GUID: k.ID, ItemType: "Lakehouse", Location: "known_lakehouses"})
		}
	}

	out := string(content)
	for oldG, newG := range replacements {
		if oldG != newG {
			out = strings.ReplaceAll(out, oldG, newG)
		}
	}
	return []byte(out), unresolved
}
