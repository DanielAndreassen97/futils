package deploy

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// fakeFabric implements FabricClient for resolver/plan/execute tests.
type fakeFabric struct {
	workspaces []fabric.Workspace
	itemsByWS  map[string][]fabric.Item // workspaceID -> items
	sqlByLH    map[string][2]string     // lakehouseID -> {host, id}
}

func (f *fakeFabric) ListWorkspaces(token string) ([]fabric.Workspace, error) { return f.workspaces, nil }
func (f *fakeFabric) ListItems(token, ws string) ([]fabric.Item, error)       { return f.itemsByWS[ws], nil }
func (f *fakeFabric) ListItemsByType(token, ws, typ string) ([]fabric.Item, error) {
	var out []fabric.Item
	for _, it := range f.itemsByWS[ws] {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *fakeFabric) GetItemDefinition(token, ws, id, format string) (*fabric.Definition, error) {
	return &fabric.Definition{}, nil
}
func (f *fakeFabric) CreateItem(token, ws, name, typ string, def *fabric.Definition) (fabric.Item, error) {
	return fabric.Item{ID: "new-" + name, DisplayName: name, Type: typ, WorkspaceID: ws}, nil
}
func (f *fakeFabric) UpdateItemDefinition(token, ws, id string, def *fabric.Definition) error { return nil }
func (f *fakeFabric) RebindReport(token, ws, reportID, datasetID string) error                { return nil }
func (f *fakeFabric) GetLakehouseSqlEndpoint(token, ws, lhID string) (string, string, error) {
	v := f.sqlByLH[lhID]
	return v[0], v[1], nil
}

func newResolverFixture() *Resolver {
	target := fabric.Workspace{ID: "ws-test", DisplayName: "DP - TEST - Config"}
	f := &fakeFabric{
		workspaces: []fabric.Workspace{target, {ID: "ws-data", DisplayName: "DP - TEST - Data"}},
		itemsByWS: map[string][]fabric.Item{
			"ws-test": {{ID: "lh-1", DisplayName: "LH_Config", Type: "Lakehouse"}},
			"ws-data": {{ID: "lh-2", DisplayName: "LH_Silver", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{"lh-1": {"abc.datawarehouse.fabric.microsoft.com", "ep-1"}},
	}
	return NewResolver(f, "tok", target)
}

func TestResolveWorkspaceId(t *testing.T) {
	r := newResolverFixture()
	got, err := r.Resolve("$workspace.$id")
	if err != nil || got != "ws-test" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestResolveItemId(t *testing.T) {
	r := newResolverFixture()
	got, err := r.Resolve("$items.Lakehouse.LH_Config.$id")
	if err != nil || got != "lh-1" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestResolveNamedWorkspaceId(t *testing.T) {
	r := newResolverFixture()
	got, err := r.Resolve("$workspace.DP - TEST - Data.$id")
	if err != nil || got != "ws-data" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestResolveSqlEndpoint(t *testing.T) {
	r := newResolverFixture()
	host, err := r.Resolve("$items.Lakehouse.LH_Config.$sqlendpoint")
	if err != nil || host != "abc.datawarehouse.fabric.microsoft.com" {
		t.Fatalf("host=%q err=%v", host, err)
	}
	id, err := r.Resolve("$items.Lakehouse.LH_Config.$sqlendpointid")
	if err != nil || id != "ep-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestResolvePassThrough(t *testing.T) {
	r := newResolverFixture()
	got, err := r.Resolve("literal-guid-1234")
	if err != nil || got != "literal-guid-1234" {
		t.Fatalf("got %q err %v", got, err)
	}
}
