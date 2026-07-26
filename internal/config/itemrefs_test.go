package config

import (
	"reflect"
	"testing"
)

// itemRefFixture names "nb_ingest" in all five places an item name can appear,
// across two customers — the second exists to prove the scan does not stop at
// the first match, and that a shared name is reported for both.
func itemRefFixture() Config {
	return Config{Customers: map[string]Customer{
		"Fabrikam": {
			Favorites: []NotebookFavorite{
				{Name: "nb_ingest", Parameters: []string{"run_date"}},
				{Name: "nb_other"},
			},
			PostDeployRuns: []string{"nb_ingest", "PL_refresh"},
			ReferenceOverrides: []ReferenceOverride{
				{SourceGUID: "guid-1", ItemType: "Notebook", ItemName: "nb_ingest"},
				{SourceGUID: "guid-2", ItemType: "Lakehouse", ItemName: "lh_bronze"},
			},
			Substitutions: []Substitution{
				{FindValue: "old-guid", TargetType: "Notebook", TargetName: "nb_ingest"},
				{FindValue: "other", ItemType: "Notebook", ItemName: "nb_ingest"},
				{FindValue: "unrelated", TargetName: "something-else"},
			},
		},
		"Contoso": {
			Favorites: []NotebookFavorite{{Name: "nb_ingest"}},
		},
	}}
}

func TestFindItemRefsCoversAllFiveKinds(t *testing.T) {
	got := FindItemRefs(itemRefFixture(), "nb_ingest")

	want := []ItemRef{
		{Customer: "Contoso", Kind: RefFavorite},
		{Customer: "Fabrikam", Kind: RefFavorite},
		{Customer: "Fabrikam", Kind: RefPostDeployRun},
		{Customer: "Fabrikam", Kind: RefOverrideTarget, Detail: "guid-1"},
		{Customer: "Fabrikam", Kind: RefSubstitutionTarget, Detail: "old-guid"},
		{Customer: "Fabrikam", Kind: RefSubstitutionFilter, Detail: "other"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindItemRefs =\n %+v\nwant\n %+v", got, want)
	}
}

func TestFindItemRefsIsSortedByCustomer(t *testing.T) {
	// Config.Customers is a map, so an unsorted scan would reshuffle the impact
	// list the user is asked to confirm.
	for i := 0; i < 20; i++ {
		got := FindItemRefs(itemRefFixture(), "nb_ingest")
		if len(got) == 0 || got[0].Customer != "Contoso" {
			t.Fatalf("unstable order on run %d: %+v", i, got)
		}
	}
}

func TestFindItemRefsNoMatch(t *testing.T) {
	if got := FindItemRefs(itemRefFixture(), "nope"); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestFindItemRefsIsCaseSensitive(t *testing.T) {
	// Fabric item names are matched exactly everywhere else in futils; a looser
	// scan here would claim references that never resolved.
	if got := FindItemRefs(itemRefFixture(), "NB_INGEST"); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestRenameItemRefsRewritesAllFiveKinds(t *testing.T) {
	cfg := itemRefFixture()

	if n := RenameItemRefs(&cfg, "nb_ingest", "nb_ingest_customers"); n != 6 {
		t.Errorf("renamed %d references, want 6", n)
	}

	fab := cfg.Customers["Fabrikam"]
	if fab.Favorites[0].Name != "nb_ingest_customers" {
		t.Errorf("favourite = %q", fab.Favorites[0].Name)
	}
	if fab.PostDeployRuns[0] != "nb_ingest_customers" {
		t.Errorf("post-deploy run = %q", fab.PostDeployRuns[0])
	}
	if fab.ReferenceOverrides[0].ItemName != "nb_ingest_customers" {
		t.Errorf("override = %q", fab.ReferenceOverrides[0].ItemName)
	}
	if fab.Substitutions[0].TargetName != "nb_ingest_customers" {
		t.Errorf("substitution target = %q", fab.Substitutions[0].TargetName)
	}
	if fab.Substitutions[1].ItemName != "nb_ingest_customers" {
		t.Errorf("substitution filter = %q", fab.Substitutions[1].ItemName)
	}
	if cfg.Customers["Contoso"].Favorites[0].Name != "nb_ingest_customers" {
		t.Error("the second customer was not rewritten")
	}
	// Untouched entries stay put.
	if fab.Favorites[1].Name != "nb_other" || fab.ReferenceOverrides[1].ItemName != "lh_bronze" {
		t.Error("an unrelated entry was rewritten")
	}
}

func TestRenameItemRefsNoMatchLeavesConfigUntouched(t *testing.T) {
	cfg, before := itemRefFixture(), itemRefFixture()

	if n := RenameItemRefs(&cfg, "nope", "whatever"); n != 0 {
		t.Errorf("renamed %d, want 0", n)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Error("config was modified despite no matches")
	}
}

func TestRemoveItemRefsDropsEntriesButOnlyClearsAFilter(t *testing.T) {
	// A substitution whose TARGET is gone cannot resolve and is removed. One
	// that merely FILTERS on the name still works without the filter — clearing
	// it widens the rule instead of throwing it away.
	cfg := itemRefFixture()

	if n := RemoveItemRefs(&cfg, "nb_ingest"); n != 6 {
		t.Errorf("removed %d references, want 6", n)
	}

	fab := cfg.Customers["Fabrikam"]
	if len(fab.Favorites) != 1 || fab.Favorites[0].Name != "nb_other" {
		t.Errorf("favourites = %+v", fab.Favorites)
	}
	if !reflect.DeepEqual(fab.PostDeployRuns, []string{"PL_refresh"}) {
		t.Errorf("post-deploy runs = %v", fab.PostDeployRuns)
	}
	if len(fab.ReferenceOverrides) != 1 || fab.ReferenceOverrides[0].ItemName != "lh_bronze" {
		t.Errorf("overrides = %+v", fab.ReferenceOverrides)
	}
	// The target-match substitution is gone; the filter-match one survives with
	// its filter cleared; the unrelated one is untouched.
	if len(fab.Substitutions) != 2 {
		t.Fatalf("substitutions = %+v, want 2", fab.Substitutions)
	}
	if fab.Substitutions[0].FindValue != "other" || fab.Substitutions[0].ItemName != "" {
		t.Errorf("filter substitution = %+v, want its ItemName cleared", fab.Substitutions[0])
	}
	if fab.Substitutions[1].FindValue != "unrelated" {
		t.Errorf("unrelated substitution = %+v", fab.Substitutions[1])
	}
	if len(cfg.Customers["Contoso"].Favorites) != 0 {
		t.Error("the second customer's favourite was not removed")
	}
}

func TestRemoveItemRefsNoMatchLeavesConfigUntouched(t *testing.T) {
	cfg, before := itemRefFixture(), itemRefFixture()

	if n := RemoveItemRefs(&cfg, "nope"); n != 0 {
		t.Errorf("removed %d, want 0", n)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Error("config was modified despite no matches")
	}
}

func TestItemRefKindString(t *testing.T) {
	cases := map[ItemRefKind]string{
		RefFavorite:           "favourite",
		RefPostDeployRun:      "post-deploy run",
		RefOverrideTarget:     "reference override",
		RefSubstitutionTarget: "substitution target",
		RefSubstitutionFilter: "substitution filter",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}
