package cmd

import (
	"reflect"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestFilterParamsByFavorite_NoFavoritesReturnsAll(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	customer := config.Customer{} // no favourites at all

	got := filterParamsByFavorite(params, customer, "NB_X")
	if !reflect.DeepEqual(got, params) {
		t.Errorf("expected all params returned, got %#v", got)
	}
}

func TestFilterParamsByFavorite_FavoriteWithoutPinnedParamsReturnsAll(t *testing.T) {
	// An entry in Favorites without a Parameters slice means "this
	// notebook is pinned, but show every parameter". Regression guard
	// for users who favourite a notebook without drilling into params.
	params := []fabric.Parameter{{Name: "a"}, {Name: "b"}}
	customer := config.Customer{
		Favorites: []config.NotebookFavorite{{Name: "NB_X"}},
	}

	got := filterParamsByFavorite(params, customer, "NB_X")
	if !reflect.DeepEqual(got, params) {
		t.Errorf("expected all params, got %#v", got)
	}
}

func TestFilterParamsByFavorite_FiltersToFavoriteSubset(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}, {Name: "delta"},
	}
	customer := config.Customer{
		Favorites: []config.NotebookFavorite{
			{Name: "NB_X", Parameters: []string{"beta", "delta"}},
		},
	}

	got := filterParamsByFavorite(params, customer, "NB_X")
	want := []fabric.Parameter{{Name: "beta"}, {Name: "delta"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expected filtered subset, got %#v", got)
	}
}

func TestFilterParamsByFavorite_PreservesNotebookOrder(t *testing.T) {
	// Even if the favourites list stores parameters in a different
	// order, the form should render them in the order they appear in
	// the notebook so the UI matches the source-of-truth.
	params := []fabric.Parameter{
		{Name: "first"}, {Name: "second"}, {Name: "third"},
	}
	customer := config.Customer{
		Favorites: []config.NotebookFavorite{
			{Name: "NB_X", Parameters: []string{"third", "first"}},
		},
	}

	got := filterParamsByFavorite(params, customer, "NB_X")
	if got[0].Name != "first" || got[1].Name != "third" {
		t.Errorf("expected notebook order, got %v", got)
	}
}

func TestFilterParamsByFavorite_DifferentNotebookReturnsAll(t *testing.T) {
	// Favourite for NB_A must not affect filtering for NB_B.
	params := []fabric.Parameter{{Name: "x"}, {Name: "y"}}
	customer := config.Customer{
		Favorites: []config.NotebookFavorite{
			{Name: "NB_A", Parameters: []string{"x"}},
		},
	}

	got := filterParamsByFavorite(params, customer, "NB_B")
	if len(got) != 2 {
		t.Errorf("expected unfiltered list for other notebook, got %#v", got)
	}
}

func TestMergeFavorites_PreservesPinnedParamsForRepeats(t *testing.T) {
	// When a user re-favourites a notebook they already had pinned, we
	// must keep the parameter filter they'd previously configured.
	existing := []config.NotebookFavorite{
		{Name: "NB_Main_Dim", Parameters: []string{"p1", "p2"}},
		{Name: "NB_GONE"},
	}
	selected := []string{"NB_Main_Dim", "NB_NEW"}

	got := mergeFavorites(selected, existing)
	if len(got) != 2 {
		t.Fatalf("expected 2 favourites, got %d", len(got))
	}
	if got[0].Name != "NB_Main_Dim" || len(got[0].Parameters) != 2 {
		t.Errorf("expected NB_Main_Dim with 2 pinned params, got %#v", got[0])
	}
	if got[1].Name != "NB_NEW" || len(got[1].Parameters) != 0 {
		t.Errorf("expected fresh NB_NEW with no params, got %#v", got[1])
	}
}

func TestMergeFavorites_DropsUnselectedEntries(t *testing.T) {
	// A notebook the user un-ticks in the multi-select should be gone
	// from the merged list — including any pinned params.
	existing := []config.NotebookFavorite{
		{Name: "NB_A", Parameters: []string{"x"}},
		{Name: "NB_B"},
	}
	selected := []string{"NB_A"} // dropped NB_B

	got := mergeFavorites(selected, existing)
	if len(got) != 1 || got[0].Name != "NB_A" {
		t.Errorf("expected only NB_A, got %#v", got)
	}
}

func TestResolveDefaultLakehouse_FoundInRunWorkspace(t *testing.T) {
	f := &fakeMoveAPI{
		workspaces: []fabric.Workspace{{ID: "ws-run"}, {ID: "ws-other"}},
		items: map[string][]fabric.Item{
			"ws-run": {{ID: "lh-1", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
	}
	lh, err := resolveDefaultLakehouse(f, "tok", "lh-1", "ws-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lh == nil || lh.WorkspaceID != "ws-run" || lh.ID != "lh-1" || lh.Name != "LH_ConfigLog" {
		t.Errorf("expected lakehouse resolved to run workspace, got %#v", lh)
	}
}

func TestResolveDefaultLakehouse_FoundInAnotherWorkspace(t *testing.T) {
	// The whole point of resolving rather than guessing: a default lakehouse
	// can live in a different workspace than the one we run in.
	f := &fakeMoveAPI{
		workspaces: []fabric.Workspace{{ID: "ws-run"}, {ID: "ws-shared"}},
		items: map[string][]fabric.Item{
			"ws-run":    {{ID: "other-lh", DisplayName: "LH_Local", Type: "Lakehouse"}},
			"ws-shared": {{ID: "lh-1", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
	}
	lh, err := resolveDefaultLakehouse(f, "tok", "lh-1", "ws-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lh == nil || lh.WorkspaceID != "ws-shared" {
		t.Errorf("expected resolution to the shared workspace, got %#v", lh)
	}
}

func TestResolveDefaultLakehouse_NotFoundErrors(t *testing.T) {
	// Lakehouse is nowhere the user can see → a clear error, never a guessed
	// workspace.
	f := &fakeMoveAPI{
		workspaces: []fabric.Workspace{{ID: "ws-run"}},
		items:      map[string][]fabric.Item{},
	}
	if _, err := resolveDefaultLakehouse(f, "tok", "lh-gone", "ws-run"); err == nil {
		t.Error("expected an error when the lakehouse can't be found, got nil")
	}
}

func TestResolveLakehouseOverride_CompleteBindingIsNoOp(t *testing.T) {
	// A notebook whose binding already carries a workspace must produce no
	// override — futils only intervenes on the broken pattern.
	ipynb := []byte(`{"metadata":{"dependencies":{"lakehouse":{` +
		`"default_lakehouse":"lh-1","default_lakehouse_name":"LH","default_lakehouse_workspace_id":"ws-run"}}}}`)
	f := &fakeMoveAPI{}
	lh, err := resolveLakehouseOverride(f, "tok", ipynb, "ws-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lh != nil {
		t.Errorf("expected no override for a complete binding, got %#v", lh)
	}
}

func TestResolveLakehouseOverride_NoLakehouseIsNoOp(t *testing.T) {
	// No lakehouse pinned → run with none, never invent one.
	ipynb := []byte(`{"metadata":{"kernelspec":{"name":"synapse_pyspark"}}}`)
	f := &fakeMoveAPI{}
	lh, err := resolveLakehouseOverride(f, "tok", ipynb, "ws-run")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lh != nil {
		t.Errorf("expected no override when no lakehouse is pinned, got %#v", lh)
	}
}
