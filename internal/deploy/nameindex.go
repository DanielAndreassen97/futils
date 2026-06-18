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

// ItemByName returns the indexed item for a name+type (forward lookup). Returns
// false when the name is absent, or ambiguous across the env's workspaces.
func (i *NameIndex) ItemByName(name, typ string) (IndexedItem, bool) {
	k := nameKey{name, typ}
	if i.ambiguous[k] {
		return IndexedItem{}, false
	}
	it, ok := i.byName[k]
	return it, ok
}
