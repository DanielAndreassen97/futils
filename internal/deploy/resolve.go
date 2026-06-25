package deploy

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Resolver expands fabric-cicd dynamic variables against the TARGET workspace
// at deploy time. It caches workspace and item lookups so repeated variables
// don't re-hit the API.
//
// The lazy caches (wsByName, itemsWS) are guarded by mu so the resolver is safe
// to share across the concurrent per-item compare workers. client/token/target
// are set at construction and never mutated, so they need no lock.
type Resolver struct {
	client   FabricClient
	token    string
	target   fabric.Workspace
	mu       sync.Mutex
	wsByName map[string]string        // displayName -> id (lazy, guarded by mu)
	itemsWS  map[string][]fabric.Item // workspaceID -> items (lazy, guarded by mu)
}

func NewResolver(client FabricClient, token string, target fabric.Workspace) *Resolver {
	return &Resolver{
		client:  client,
		token:   token,
		target:  target,
		itemsWS: map[string][]fabric.Item{},
	}
}

// Resolve expands a single dynamic-variable value. A value that doesn't begin
// with "$" is returned unchanged (literal replacement value).
func (r *Resolver) Resolve(value string) (string, error) {
	if !strings.HasPrefix(value, "$") {
		return value, nil
	}
	switch {
	case value == "$workspace.$id":
		return r.target.ID, nil
	case strings.HasPrefix(value, "$workspace.") && strings.Contains(value, ".$items."):
		// $workspace.<name>.$items.<Type>.<Name>.<attr>
		rest := strings.TrimPrefix(value, "$workspace.")
		i := strings.Index(rest, ".$items.")
		wsName := rest[:i]
		itemExpr := rest[i+1:] // "$items.<Type>.<Name>.<attr>"
		wsID, err := r.workspaceID(wsName)
		if err != nil {
			return "", err
		}
		return r.resolveItemIn(wsID, itemExpr)
	case strings.HasPrefix(value, "$workspace.") && strings.HasSuffix(value, ".$id"):
		name := strings.TrimSuffix(strings.TrimPrefix(value, "$workspace."), ".$id")
		return r.workspaceID(name)
	case strings.HasPrefix(value, "$items."):
		return r.resolveItemIn(r.target.ID, value)
	default:
		return "", fmt.Errorf("unsupported dynamic variable %q (supports $workspace/$items)", value)
	}
}

// resolveItemIn handles "$items.<Type>.<Name>.<attr>" against a specific
// workspace. attr is $id, $sqlendpoint, or $sqlendpointid. Name may contain
// dots, so the known trailing attribute is stripped before splitting the type.
func (r *Resolver) resolveItemIn(wsID, value string) (string, error) {
	body := strings.TrimPrefix(value, "$items.")
	var attr string
	for _, a := range []string{".$sqlendpointid", ".$sqlendpoint", ".$id"} {
		if strings.HasSuffix(body, a) {
			attr = strings.TrimPrefix(a, ".$")
			body = strings.TrimSuffix(body, a)
			break
		}
	}
	if attr == "" {
		return "", fmt.Errorf("dynamic variable %q has no recognised attribute", value)
	}
	dot := strings.Index(body, ".")
	if dot < 0 {
		return "", fmt.Errorf("dynamic variable %q missing item name", value)
	}
	itemType, itemName := body[:dot], body[dot+1:]

	item, err := r.findItem(wsID, itemType, itemName)
	if err != nil {
		return "", err
	}
	switch attr {
	case "id":
		return item.ID, nil
	case "sqlendpoint", "sqlendpointid":
		host, id, err := r.client.GetLakehouseSqlEndpoint(r.token, wsID, item.ID)
		if err != nil {
			return "", fmt.Errorf("sql endpoint for %s: %w", itemName, err)
		}
		if attr == "sqlendpoint" {
			return host, nil
		}
		return id, nil
	}
	return "", fmt.Errorf("unreachable")
}

func (r *Resolver) workspaceID(name string) (string, error) {
	// Lock spans the check-and-fill so two workers can't both call
	// ListWorkspaces. The wsByName nil-check ensures ListWorkspaces runs at most
	// once per Resolver, so holding the lock across that single first call is
	// acceptable — subsequent calls return immediately on the already-populated
	// cache.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wsByName == nil {
		ws, err := r.client.ListWorkspaces(r.token)
		if err != nil {
			return "", err
		}
		r.wsByName = make(map[string]string, len(ws))
		for _, w := range ws {
			r.wsByName[w.DisplayName] = w.ID
		}
	}
	id, ok := r.wsByName[name]
	if !ok {
		return "", fmt.Errorf("workspace %q not found", name)
	}
	return id, nil
}

func (r *Resolver) findItem(wsID, itemType, itemName string) (fabric.Item, error) {
	// Lock spans the check-and-fill (see workspaceID for the rationale).
	r.mu.Lock()
	items, ok := r.itemsWS[wsID]
	if !ok {
		var err error
		items, err = r.client.ListItems(r.token, wsID)
		if err != nil {
			r.mu.Unlock()
			return fabric.Item{}, err
		}
		r.itemsWS[wsID] = items
	}
	r.mu.Unlock()
	for _, it := range items {
		if it.Type == itemType && it.DisplayName == itemName {
			return it, nil
		}
	}
	return fabric.Item{}, fmt.Errorf("%s %q not found in workspace %s", itemType, itemName, wsID)
}
