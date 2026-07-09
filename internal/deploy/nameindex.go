package deploy

import (
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// IndexedItem is one item located in an environment's name index.
type IndexedItem struct {
	Name        string
	Type        string
	GUID        string
	WorkspaceID string
}

type nameKey struct {
	name string
	typ  string
}

// NameIndex maps a single environment's items both ways: GUID->item (to learn
// the name of a baseline GUID) and name+type->item (to find that name's GUID in
// a target env). A name+type appearing in more than one of the env's workspaces
// is recorded as ambiguous and will not resolve forward — matching by name is
// only safe when names are unique within the env.
type NameIndex struct {
	byGUID    map[string]IndexedItem
	byName    map[nameKey]IndexedItem
	ambiguous map[nameKey]bool
}

// BuildNameIndex enumerates every workspace's items (one ListItems call each)
// and indexes them. Each item's WorkspaceID is set to the workspace it was
// listed from, since the items endpoint does not reliably echo it back.
func BuildNameIndex(client FabricClient, token string, workspaces []fabric.Workspace) (*NameIndex, error) {
	idx := &NameIndex{
		byGUID:    map[string]IndexedItem{},
		byName:    map[nameKey]IndexedItem{},
		ambiguous: map[nameKey]bool{},
	}
	for _, ws := range workspaces {
		items, err := client.ListItems(token, ws.ID)
		if err != nil {
			return nil, fmt.Errorf("index workspace %q: %w", ws.DisplayName, err)
		}
		for _, it := range items {
			ent := IndexedItem{Name: it.DisplayName, Type: it.Type, GUID: it.ID, WorkspaceID: ws.ID}
			idx.byGUID[it.ID] = ent
			k := nameKey{it.DisplayName, it.Type}
			// Callers pass a de-duplicated workspace set (e.g. via
			// config.Environment.AllWorkspaces), so a key seen twice with the
			// SAME GUID is just the same item re-listed and is harmless to
			// overwrite; only a DIFFERENT GUID under the same name+type is a
			// genuine ambiguity worth blocking forward resolution.
			if prev, ok := idx.byName[k]; ok && prev.GUID != it.ID {
				idx.ambiguous[k] = true
				continue
			}
			idx.byName[k] = ent
		}
	}
	return idx, nil
}

// ItemByGUID returns the indexed item for a GUID (reverse lookup).
func (i *NameIndex) ItemByGUID(guid string) (IndexedItem, bool) {
	it, ok := i.byGUID[guid]
	return it, ok
}

// ItemsOfType returns every indexed item of the given type. Order is
// unspecified (map iteration).
func (i *NameIndex) ItemsOfType(typ string) []IndexedItem {
	var out []IndexedItem
	for _, it := range i.byGUID {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out
}

// LookupStatus distinguishes the three forward-lookup outcomes so callers can
// explain WHY a name didn't resolve.
type LookupStatus int

const (
	LookupFound LookupStatus = iota
	LookupAbsent
	LookupAmbiguous
)

// LookupName resolves a name+type forward, reporting whether it was found,
// absent, or ambiguous across the env's workspaces.
func (i *NameIndex) LookupName(name, typ string) (IndexedItem, LookupStatus) {
	k := nameKey{name, typ}
	if i.ambiguous[k] {
		return IndexedItem{}, LookupAmbiguous
	}
	it, ok := i.byName[k]
	if !ok {
		return IndexedItem{}, LookupAbsent
	}
	return it, LookupFound
}

// ItemByName returns the indexed item for a name+type (forward lookup). Returns
// false when the name is absent, or ambiguous across the env's workspaces.
func (i *NameIndex) ItemByName(name, typ string) (IndexedItem, bool) {
	it, st := i.LookupName(name, typ)
	return it, st == LookupFound
}
