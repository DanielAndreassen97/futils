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
