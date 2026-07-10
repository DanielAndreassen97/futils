package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadConfigReturnsEmptyWhenNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Customers) != 0 {
		t.Errorf("expected 0 customers, got %d", len(cfg.Customers))
	}
}

func TestAddAndListCustomer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := AddCustomer(path, "Contoso", Customer{
		Environments: []Environment{
			{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(path)
	if len(cfg.Customers) != 1 {
		t.Fatalf("expected 1 customer, got %d", len(cfg.Customers))
	}
	c := cfg.Customers["Contoso"]
	if len(c.Environments) != 1 || c.Environments[0].Alias != "DEV" {
		t.Errorf("unexpected customer shape: %#v", c)
	}
}

func TestRemoveCustomer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	AddCustomer(path, "Contoso", Customer{
		Environments: []Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}},
	})
	if err := RemoveCustomer(path, "Contoso"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(path)
	if len(cfg.Customers) != 0 {
		t.Errorf("expected 0 customers, got %d", len(cfg.Customers))
	}
}

func TestRemoveNonexistentCustomerReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := RemoveCustomer(path, "Ghost"); err == nil {
		t.Error("expected error, got nil")
	}
}

func TestGetConfigPath(t *testing.T) {
	p := GetConfigPath()
	if p == "" {
		t.Error("expected non-empty config path")
	}
	if filepath.Base(p) != "config.json" {
		t.Errorf("expected config.json, got %s", filepath.Base(p))
	}
}

func TestEditCustomer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	AddCustomer(path, "Contoso", Customer{
		Environments: []Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}},
	})
	err := EditCustomer(path, "Contoso", Customer{
		Environments: []Environment{
			{Alias: "DEV", Workspaces: []string{"NW - DEV - Analytics"}},
			{Alias: "PROD", Workspaces: []string{"NW - PROD - Analytics"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(path)
	c := cfg.Customers["Contoso"]
	if len(c.Environments) != 2 {
		t.Errorf("expected 2 environments, got %d", len(c.Environments))
	}
	if len(c.Environments[0].Workspaces) != 1 || c.Environments[0].Workspaces[0] != "NW - DEV - Analytics" {
		t.Errorf("expected single workspace NW - DEV - Analytics, got %v", c.Environments[0].Workspaces)
	}
}

func TestEditNonexistentCustomerReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	err := EditCustomer(path, "Ghost", Customer{})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSetFavoritesRoundtrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	AddCustomer(path, "Fabrikam", Customer{
		Environments: []Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}},
	})

	favs := []NotebookFavorite{
		{Name: "NB_Main_Dim", Parameters: []string{"specific_table_names", "rewrite_table"}},
		{Name: "NB_Main_Fakta"},
	}
	if err := SetFavorites(path, "Fabrikam", favs); err != nil {
		t.Fatal(err)
	}

	cfg, _ := Load(path)
	got := cfg.Customers["Fabrikam"].Favorites
	if len(got) != 2 {
		t.Fatalf("expected 2 favourites, got %d", len(got))
	}
	if got[0].Name != "NB_Main_Dim" || len(got[0].Parameters) != 2 {
		t.Errorf("first favourite roundtrip wrong: %#v", got[0])
	}
	if got[1].Name != "NB_Main_Fakta" || len(got[1].Parameters) != 0 {
		t.Errorf("second favourite roundtrip wrong: %#v", got[1])
	}
}

func TestSetFavoritesUnknownCustomerReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SetFavorites(path, "Ghost", []NotebookFavorite{{Name: "X"}}); err == nil {
		t.Error("expected error for unknown customer, got nil")
	}
}

func TestFavoriteFor(t *testing.T) {
	c := Customer{
		Favorites: []NotebookFavorite{
			{Name: "NB_A", Parameters: []string{"p1"}},
			{Name: "NB_B"},
		},
	}
	if got, ok := c.FavoriteFor("NB_A"); !ok || len(got.Parameters) != 1 {
		t.Errorf("NB_A lookup wrong: %#v, ok=%v", got, ok)
	}
	if _, ok := c.FavoriteFor("NB_MISSING"); ok {
		t.Error("expected miss for NB_MISSING, got hit")
	}
}

func TestFavoriteNamesPreservesOrder(t *testing.T) {
	c := Customer{
		Favorites: []NotebookFavorite{
			{Name: "zeta"}, {Name: "alpha"}, {Name: "mu"},
		},
	}
	got := c.FavoriteNames()
	want := []string{"zeta", "alpha", "mu"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order mismatch at %d: %q vs %q", i, got[i], want[i])
		}
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	path := filepath.Join(dir, "config.json")
	err := AddCustomer(path, "Test", Customer{
		Environments: []Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected config file to be created")
	}
}

func TestWorkspacesLookup(t *testing.T) {
	c := Customer{
		Environments: []Environment{
			{Alias: "DEV", Workspaces: []string{"DW - DEV - Config", "DW - DEV - SemMod"}},
			{Alias: "feature", Workspaces: []string{"DW - feature/jane - Config"}},
		},
	}
	if got, ok := c.Workspaces("DEV"); !ok || len(got) != 2 || got[0] != "DW - DEV - Config" || got[1] != "DW - DEV - SemMod" {
		t.Errorf("DEV lookup: got %v ok=%v", got, ok)
	}
	if got, ok := c.Workspaces("feature"); !ok || len(got) != 1 || got[0] != "DW - feature/jane - Config" {
		t.Errorf("feature lookup: got %v ok=%v", got, ok)
	}
	if _, ok := c.Workspaces("PROD"); ok {
		t.Error("expected miss for PROD, got hit")
	}
}

func TestLoadMigratesLegacyConfig(t *testing.T) {
	legacy := `{
		"customers": {
			"Fabrikam": {
				"workspace_pattern": "DW - {env} - Config",
				"environments": ["DEV", "TEST", "PROD"],
				"favorites": [{"name": "NB_A", "parameters": ["p1"]}]
			}
		}
	}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	c := cfg.Customers["Fabrikam"]
	if len(c.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(c.Environments))
	}
	want := []Environment{
		{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
		{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}},
		{Alias: "PROD", Workspaces: []string{"DW - PROD - Config"}},
	}
	if !reflect.DeepEqual(c.Environments, want) {
		t.Errorf("environments = %#v, want %#v", c.Environments, want)
	}
	if len(c.Favorites) != 1 || c.Favorites[0].Name != "NB_A" {
		t.Errorf("favourites lost during migration: %#v", c.Favorites)
	}
}

func TestSaveWritesNewShapeAfterMigration(t *testing.T) {
	legacy := `{"customers":{"X":{"workspace_pattern":"A - {env} - B","environments":["DEV"]}}}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _ := Load(path)
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	s := string(raw)
	if strings.Contains(s, "workspace_pattern") {
		t.Errorf("expected workspace_pattern to be dropped, got: %s", s)
	}
	if !strings.Contains(s, `"alias"`) || !strings.Contains(s, `"workspaces"`) {
		t.Errorf("expected new shape keys, got: %s", s)
	}

	var round Config
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("re-unmarshal failed: %v", err)
	}
}

// TestLoadMigratesSingleWorkspaceShape covers the intermediate shape
// from before multi-workspace per env — environments: [{alias,
// workspace_name: string}] — which gets folded into a one-element
// Workspaces slice. Real user configs in the wild are on this shape
// before the futils v0.1 → v0.2 upgrade, so the migration must hold.
func TestCustomerRepoPathRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	in := Config{Customers: map[string]Customer{
		"acme": {RepoPath: "/home/dan/repos/acme"},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := out.Customers["acme"].RepoPath; got != "/home/dan/repos/acme" {
		t.Errorf("RepoPath = %q, want %q", got, "/home/dan/repos/acme")
	}
}

func TestCustomerWithoutRepoPathLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Legacy config with no repo_path field at all.
	if err := os.WriteFile(path, []byte(`{"customers":{"acme":{"environments":[]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.Customers["acme"].RepoPath != "" {
		t.Errorf("RepoPath should default to empty, got %q", out.Customers["acme"].RepoPath)
	}
}

func TestDeploymentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {Environments: []Environment{{
			Alias:      "DEV",
			Workspaces: []string{"DW - DEV - Config", "DW - DEV - SemMod"},
			Deployments: []DeployMapping{
				{Folder: "Backend", Workspace: "DW - DEV - Config"},
				{Folder: "Frontend", Workspace: "DW - DEV - SemMod"},
			},
		}}},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	maps, ok := out.Customers["acme"].DeployMappings("DEV")
	if !ok || len(maps) != 2 {
		t.Fatalf("DeployMappings = %v ok=%v", maps, ok)
	}
	if maps[0].Folder != "Backend" || maps[0].Workspace != "DW - DEV - Config" {
		t.Errorf("maps[0] = %+v", maps[0])
	}
}

func TestDeploymentsAbsentLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"customers":{"acme":{"environments":[{"alias":"DEV","workspaces":["A"]}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	maps, ok := out.Customers["acme"].DeployMappings("DEV")
	if !ok || len(maps) != 0 {
		t.Errorf("expected env found with no deployments, got %v ok=%v", maps, ok)
	}
}

func TestLoadMigratesSingleWorkspaceShape(t *testing.T) {
	legacy := `{
		"customers": {
			"Acme": {
				"environments": [
					{"alias": "DEV", "workspace_name": "Acme - DEV - DM"},
					{"alias": "PROD", "workspace_name": "Acme - PROD - DM"}
				]
			}
		}
	}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Environment{
		{Alias: "DEV", Workspaces: []string{"Acme - DEV - DM"}},
		{Alias: "PROD", Workspaces: []string{"Acme - PROD - DM"}},
	}
	if !reflect.DeepEqual(cfg.Customers["Acme"].Environments, want) {
		t.Errorf("environments = %#v, want %#v", cfg.Customers["Acme"].Environments, want)
	}
}

func TestReferenceOverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {
			Environments:        []Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}},
			BaselineEnvironment: "DEV",
			ReferenceOverrides: []ReferenceOverride{
				{SourceGUID: "0b0b0b0b-1111-2222-3333-444455556666", ItemType: "Lakehouse", ItemName: "LH_Silver", Note: "cross-workspace"},
			},
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := out.Customers["acme"]
	if c.BaselineEnvironment != "DEV" {
		t.Errorf("BaselineEnvironment = %q, want DEV", c.BaselineEnvironment)
	}
	if len(c.ReferenceOverrides) != 1 || c.ReferenceOverrides[0].ItemName != "LH_Silver" {
		t.Fatalf("ReferenceOverrides = %#v", c.ReferenceOverrides)
	}
	if c.ReferenceOverrides[0].SourceGUID != "0b0b0b0b-1111-2222-3333-444455556666" {
		t.Errorf("SourceGUID = %q", c.ReferenceOverrides[0].SourceGUID)
	}
}

func TestExcludedItemTypesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {
			Environments:      []Environment{{Alias: "DEV", Workspaces: []string{"WS"}}},
			ExcludedItemTypes: []string{"Lakehouse", "Report"},
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := out.Customers["acme"].ExcludedItemTypes
	if len(got) != 2 || got[0] != "Lakehouse" || got[1] != "Report" {
		t.Fatalf("ExcludedItemTypes = %#v, want [Lakehouse Report]", got)
	}
}

func TestReferenceOverridesAbsentLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"customers":{"acme":{"environments":[{"alias":"DEV","workspaces":["A"]}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	c := out.Customers["acme"]
	if c.BaselineEnvironment != "" {
		t.Errorf("expected empty BaselineEnvironment, got %q", c.BaselineEnvironment)
	}
	if len(c.ReferenceOverrides) != 0 {
		t.Errorf("expected no overrides, got %#v", c.ReferenceOverrides)
	}
}

func TestAllWorkspacesUnionsAndDedupes(t *testing.T) {
	e := Environment{
		Alias:      "DEV",
		Workspaces: []string{"DW - DEV - Config", "DW - DEV - Data"},
		Deployments: []DeployMapping{
			{Folder: "Backend", Workspace: "DW - DEV - Config"},  // dup of a Workspaces entry
			{Folder: "Frontend", Workspace: "DW - DEV - SemMod"}, // new
		},
	}
	got := e.AllWorkspaces()
	want := []string{"DW - DEV - Config", "DW - DEV - Data", "DW - DEV - SemMod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllWorkspaces() = %#v, want %#v", got, want)
	}
}

func TestOverrideForGUID(t *testing.T) {
	c := Customer{ReferenceOverrides: []ReferenceOverride{
		{SourceGUID: "guid-a", ItemType: "Lakehouse", ItemName: "LH_Silver"},
	}}
	got, ok := c.OverrideForGUID("guid-a")
	if !ok || got.ItemName != "LH_Silver" {
		t.Fatalf("OverrideForGUID(guid-a) = %#v ok=%v", got, ok)
	}
	if _, ok := c.OverrideForGUID("missing"); ok {
		t.Error("expected missing GUID to return ok=false")
	}
}

func TestEnvironmentByAlias(t *testing.T) {
	c := Customer{Environments: []Environment{
		{Alias: "DEV", Workspaces: []string{"A"}},
		{Alias: "PROD", Workspaces: []string{"B"}},
	}}
	got, ok := c.EnvironmentByAlias("PROD")
	if !ok || len(got.Workspaces) != 1 || got.Workspaces[0] != "B" {
		t.Fatalf("EnvironmentByAlias(PROD) = %#v ok=%v", got, ok)
	}
	if _, ok := c.EnvironmentByAlias("STAGE"); ok {
		t.Error("expected unknown alias to return ok=false")
	}
}

func TestIgnoredReferencesRoundTripAndLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {
			Environments:      []Environment{{Alias: "DEV", Workspaces: []string{"A"}}},
			IgnoredReferences: []string{"guid-x", "guid-y"},
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, _ := Load(path)
	c := out.Customers["acme"]
	if len(c.IgnoredReferences) != 2 {
		t.Fatalf("IgnoredReferences = %#v", c.IgnoredReferences)
	}
	if !c.IsIgnored("guid-x") || c.IsIgnored("guid-z") {
		t.Errorf("IsIgnored wrong: x=%v z=%v", c.IsIgnored("guid-x"), c.IsIgnored("guid-z"))
	}
}

func TestIgnoredReferencesAbsentLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"customers":{"acme":{"environments":[{"alias":"DEV","workspaces":["A"]}]}}}`), 0o600)
	out, _ := Load(path)
	if len(out.Customers["acme"].IgnoredReferences) != 0 {
		t.Error("expected no ignored references on legacy config")
	}
}

func TestSubstitutionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {
			Environments: []Environment{{Alias: "DEV", Workspaces: []string{"A"}}},
			Substitutions: []Substitution{
				{FindValue: "dev-host.datawarehouse.fabric.microsoft.com", TargetType: "Lakehouse", TargetName: "LH_Config", Attr: "sqlendpoint"},
				{FindValue: "literal-find", Literal: "literal-replace"},
			},
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, _ := Load(path)
	subs := out.Customers["acme"].Substitutions
	if len(subs) != 2 {
		t.Fatalf("Substitutions = %#v", subs)
	}
	if subs[0].TargetName != "LH_Config" || subs[0].Attr != "sqlendpoint" {
		t.Errorf("subs[0] = %#v", subs[0])
	}
	if subs[1].Literal != "literal-replace" {
		t.Errorf("subs[1] = %#v", subs[1])
	}
}

func TestSubstitutionsAbsentLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"customers":{"acme":{"environments":[{"alias":"DEV","workspaces":["A"]}]}}}`), 0o600)
	out, _ := Load(path)
	if len(out.Customers["acme"].Substitutions) != 0 {
		t.Error("expected no substitutions on legacy config")
	}
}

func TestDeployHistoryPathRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := Config{Customers: map[string]Customer{
		"acme": {
			Environments:      []Environment{{Alias: "DEV", Workspaces: []string{"WS"}}},
			DeployHistoryPath: "BackEnd/deploymenthistory",
		},
	}}
	if err := Save(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := out.Customers["acme"].DeployHistoryPath; got != "BackEnd/deploymenthistory" {
		t.Fatalf("DeployHistoryPath = %q, want %q", got, "BackEnd/deploymenthistory")
	}
}

// PostDeployRuns must survive a marshal→unmarshal round trip. Customer has a
// custom UnmarshalJSON; a field missing from its aux struct silently drops.
func TestCustomerPostDeployRunsRoundTrip(t *testing.T) {
	in := Customer{PostDeployRuns: []string{"NB_Config", "NB_InsertScript_A"}}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Customer
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out.PostDeployRuns, in.PostDeployRuns) {
		t.Fatalf("PostDeployRuns = %v, want %v", out.PostDeployRuns, in.PostDeployRuns)
	}
}

// A per-mapping Repo must survive a Customer marshal→unmarshal round trip
// (Deployments decode through the custom UnmarshalJSON's environments path).
func TestDeployMappingRepoRoundTrip(t *testing.T) {
	in := Customer{
		RepoPath: "/repos/primary",
		Environments: []Environment{{
			Alias:      "DEV",
			Workspaces: []string{"WS-A", "WS-B"},
			Deployments: []DeployMapping{
				{Folder: "FabricBackEnd", Workspace: "WS-A"},             // no Repo → primary
				{Folder: "", Workspace: "WS-B", Repo: "/repos/frontend"}, // explicit repo
			},
		}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Customer
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(out.Environments, in.Environments) {
		t.Fatalf("Deployments round-trip mismatch:\n got %+v\nwant %+v", out.Environments, in.Environments)
	}
}

func TestMappingRepoFallback(t *testing.T) {
	c := Customer{RepoPath: "/repos/primary"}
	if got := c.MappingRepo(DeployMapping{Folder: "X", Workspace: "W"}); got != "/repos/primary" {
		t.Fatalf("empty Repo must fall back to RepoPath, got %q", got)
	}
	if got := c.MappingRepo(DeployMapping{Repo: "/repos/frontend"}); got != "/repos/frontend" {
		t.Fatalf("explicit Repo must win, got %q", got)
	}
}
