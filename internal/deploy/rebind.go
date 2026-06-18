package deploy

import (
	"path"
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

// RebindChange records one applied baseline→target rewrite, for the deploy
// summary. Kind is "Lakehouse", "Workspace", or "SQL endpoint".
type RebindChange struct {
	Kind string
	Old  string
	New  string
}

// RebindOutcome bundles what a rebind pass produced: the applied changes (for
// the summary, deduped by Old within a pass) and the references it could not
// resolve (surfaced to the user).
type RebindOutcome struct {
	Changes    []RebindChange
	Unresolved []UnresolvedRef
}

// Rebinder translates baseline-environment GUIDs to a target env by item name.
type Rebinder struct {
	client    FabricClient
	token     string
	baseline  *NameIndex
	target    *NameIndex
	overrides map[string]Override // baseline GUID -> override

	baseEndpoints  map[string]IndexedItem // baseline SQL-endpoint id -> lakehouse (lazy)
	targetEndpoint map[string][2]string   // target lakehouse GUID -> {host, id} (cache)
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
	return &Rebinder{client: client, token: token, baseline: b, target: t, overrides: overrides}, nil
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

// RebindPart dispatches a single item part to the right rebind pass by item
// type and part name, returning the rewritten bytes and the outcome. Parts with
// no recognized reference location are returned unchanged.
func (rb *Rebinder) RebindPart(item LocalItem, partPath string, content []byte) ([]byte, RebindOutcome) {
	if strings.HasPrefix(path.Base(partPath), "notebook-content.") {
		return rb.RebindNotebookLakehouses(content)
	}
	return content, RebindOutcome{}
}

// RebindNotebookLakehouses rewrites the lakehouse dependency GUIDs in a Fabric
// notebook part from baseline to target, by name. It only touches GUIDs found
// in the dependencies.lakehouse metadata block (never arbitrary UUIDs in code),
// records each applied rewrite as a RebindChange, and string-replaces those
// exact GUIDs throughout the content. GUIDs it cannot resolve become
// UnresolvedRef and are left unchanged. Content with no lakehouse block is
// returned unchanged with an empty outcome.
func (rb *Rebinder) RebindNotebookLakehouses(content []byte) ([]byte, RebindOutcome) {
	lh, ok := parseNotebookLakehouse(content)
	if !ok {
		return content, RebindOutcome{}
	}
	var out RebindOutcome
	seen := map[string]bool{}
	add := func(kind, oldG, newG string) {
		if oldG == "" || oldG == newG || seen[oldG] {
			return
		}
		seen[oldG] = true
		out.Changes = append(out.Changes, RebindChange{Kind: kind, Old: oldG, New: newG})
	}

	if lh.DefaultLakehouse != "" {
		var it IndexedItem
		var resolved bool
		if _, hasOverride := rb.overrides[lh.DefaultLakehouse]; !hasOverride && lh.DefaultLakehouseName != "" {
			it, resolved = rb.target.ItemByName(lh.DefaultLakehouseName, "Lakehouse")
		} else {
			it, resolved = rb.resolveGUID(lh.DefaultLakehouse)
		}
		if resolved {
			add("Lakehouse", lh.DefaultLakehouse, it.GUID)
			if lh.DefaultLakehouseWorkspaceID != "" && it.WorkspaceID != "" {
				add("Workspace", lh.DefaultLakehouseWorkspaceID, it.WorkspaceID)
			}
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: lh.DefaultLakehouse, ItemType: "Lakehouse", Location: "default_lakehouse"})
		}
	}
	for _, k := range lh.KnownLakehouses {
		if k.ID == "" || seen[k.ID] {
			continue
		}
		if it, ok := rb.resolveGUID(k.ID); ok {
			add("Lakehouse", k.ID, it.GUID)
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: k.ID, ItemType: "Lakehouse", Location: "known_lakehouses"})
		}
	}

	s := string(content)
	for _, c := range out.Changes {
		s = strings.ReplaceAll(s, c.Old, c.New)
	}
	return []byte(s), out
}
