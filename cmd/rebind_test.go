package cmd

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestBuildRebinderDisabledWithoutBaseline(t *testing.T) {
	customer := config.Customer{
		Environments: []config.Environment{{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}}},
	}
	rb, err := buildRebinder(&deployFakeAPI{}, "tok", customer, "TEST", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rb != nil {
		t.Error("expected nil rebinder when BaselineEnvironment is unset")
	}
}

func TestBuildRebinderResolvesEnvWorkspaces(t *testing.T) {
	customer := config.Customer{
		BaselineEnvironment: "DEV",
		Environments: []config.Environment{
			{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
			{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}},
		},
	}
	workspaces := []fabric.Workspace{
		{ID: "dev-config", DisplayName: "DW - DEV - Config"},
		{ID: "test-config", DisplayName: "DW - TEST - Config"},
	}
	api := &deployFakeAPI{items: map[string][]fabric.Item{
		"dev-config":  {{ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		"test-config": {{ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
	}}
	rb, err := buildRebinder(api, "tok", customer, "TEST", workspaces)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rb == nil {
		t.Fatal("expected a rebinder")
	}
	// Prove it works end-to-end: a notebook pinning the DEV ConfigLog GUID
	// rebinds to the TEST GUID by name.
	nb := []byte(`# Fabric notebook source

# METADATA ********************

# META {
# META   "dependencies": { "lakehouse": { "default_lakehouse": "dev-lh", "default_lakehouse_name": "LH_ConfigLog" } }
# META }
`)
	out, _ := rb.RebindNotebookLakehouses(nb)
	if !strings.Contains(string(out), "test-lh") {
		t.Errorf("rebinder did not translate DEV->TEST:\n%s", string(out))
	}
}

func TestBuildRebinderUnknownBaselineEnvErrors(t *testing.T) {
	customer := config.Customer{
		BaselineEnvironment: "GHOST",
		Environments:        []config.Environment{{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}}},
	}
	if _, err := buildRebinder(&deployFakeAPI{}, "tok", customer, "TEST", nil); err == nil {
		t.Error("expected error when BaselineEnvironment names no known environment")
	}
}

func TestBuildRebinderUnknownTargetEnvErrors(t *testing.T) {
	customer := config.Customer{
		BaselineEnvironment: "DEV",
		Environments: []config.Environment{
			{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
		},
	}
	if _, err := buildRebinder(&deployFakeAPI{}, "tok", customer, "GHOST", nil); err == nil {
		t.Error("expected error when target alias names no known environment")
	}
}

func TestBuildRebinderWithSubstitutionsNoBaseline(t *testing.T) {
	customer := config.Customer{
		// No BaselineEnvironment, but a substitution is defined.
		Environments:  []config.Environment{{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}}},
		Substitutions: []config.Substitution{{FindValue: "x", Literal: "y"}},
	}
	workspaces := []fabric.Workspace{{ID: "test-config", DisplayName: "DW - TEST - Config"}}
	api := &deployFakeAPI{items: map[string][]fabric.Item{"test-config": {}}}
	rb, err := buildRebinder(api, "tok", customer, "TEST", workspaces)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rb == nil {
		t.Fatal("expected a rebinder when substitutions are present even without a baseline")
	}
	// The substitution applies (literal) with no baseline.
	out, _ := rb.ApplyCustomSubstitutions(deploy.LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte("x"))
	if string(out) != "y" {
		t.Errorf("substitution not active: %q", out)
	}
}

func TestBuildRebinderNilWhenNothingToDo(t *testing.T) {
	customer := config.Customer{Environments: []config.Environment{{Alias: "TEST", Workspaces: []string{"W"}}}}
	rb, err := buildRebinder(&deployFakeAPI{}, "tok", customer, "TEST", nil)
	if err != nil || rb != nil {
		t.Errorf("expected (nil,nil) when no baseline and no substitutions; got rb=%v err=%v", rb, err)
	}
}

// TestRebinderSetIsolatedMapping proves the per-mapping baseline: two baseline
// workspaces both hold "LH_Data", the target env spans two workspaces that
// both hold "LH_Data" — the env-level rebinder would go ambiguous, but the
// isolated mapping resolves within its own (baseline ws → deploy ws) pair.
func TestRebinderSetIsolatedMapping(t *testing.T) {
	customer := config.Customer{
		BaselineEnvironment: "DEV",
		Environments: []config.Environment{
			{Alias: "DEV", Workspaces: []string{"DW - DEV - Data"}},
			{Alias: "TEST", Workspaces: []string{"DW - TEST - Data", "Front - TEST"}},
		},
	}
	workspaces := []fabric.Workspace{
		{ID: "dev-data", DisplayName: "DW - DEV - Data"},
		{ID: "test-data", DisplayName: "DW - TEST - Data"},
		{ID: "front-dev", DisplayName: "Front - DEV"},
		{ID: "front-test", DisplayName: "Front - TEST"},
	}
	api := &deployFakeAPI{items: map[string][]fabric.Item{
		"dev-data":   {{ID: "back-dev-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
		"test-data":  {{ID: "back-test-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
		"front-dev":  {{ID: "front-dev-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
		"front-test": {{ID: "front-test-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
	}}
	rs, err := newRebinderSet(api, "tok", customer, "TEST", workspaces)
	if err != nil {
		t.Fatalf("newRebinderSet: %v", err)
	}

	shared, err := rs.For(config.DeployMapping{Folder: "Backend", Workspace: "DW - TEST - Data"})
	if err != nil {
		t.Fatalf("For(shared): %v", err)
	}
	if shared != rs.shared || shared == nil {
		t.Fatal("mapping without BaselineWorkspace must get the shared env rebinder")
	}

	iso, err := rs.For(config.DeployMapping{Folder: "", Workspace: "Front - TEST", BaselineWorkspace: "Front - DEV"})
	if err != nil {
		t.Fatalf("For(isolated): %v", err)
	}
	if iso == shared {
		t.Fatal("isolated mapping must get its own rebinder")
	}
	nb := []byte(`# Fabric notebook source

# METADATA ********************

# META {
# META   "dependencies": { "lakehouse": { "default_lakehouse": "front-dev-lh", "default_lakehouse_name": "LH_Data" } }
# META }
`)
	out, outcome := iso.RebindNotebookLakehouses(nb)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("isolated resolve failed: %+v", outcome.Unresolved)
	}
	if !strings.Contains(string(out), "front-test-lh") {
		t.Errorf("want front-test-lh (isolated target), got:\n%s", string(out))
	}

	again, _ := rs.For(config.DeployMapping{Folder: "", Workspace: "Front - TEST", BaselineWorkspace: "Front - DEV"})
	if again != iso {
		t.Error("isolated rebinder must be cached per (baseline ws, target ws) pair")
	}
}

// TestRebinderSetIsolatedWithoutEnvBaseline: a mapping-level baseline works
// even when the customer has no baseline environment at all.
func TestRebinderSetIsolatedWithoutEnvBaseline(t *testing.T) {
	customer := config.Customer{
		Environments: []config.Environment{
			{Alias: "TEST", Workspaces: []string{"Front - TEST"}},
		},
	}
	workspaces := []fabric.Workspace{
		{ID: "front-dev", DisplayName: "Front - DEV"},
		{ID: "front-test", DisplayName: "Front - TEST"},
	}
	api := &deployFakeAPI{items: map[string][]fabric.Item{
		"front-dev":  {{ID: "front-dev-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
		"front-test": {{ID: "front-test-lh", DisplayName: "LH_Data", Type: "Lakehouse"}},
	}}
	rs, err := newRebinderSet(api, "tok", customer, "TEST", workspaces)
	if err != nil {
		t.Fatalf("newRebinderSet: %v", err)
	}
	if rs.shared != nil {
		t.Fatal("shared rebinder should be nil without baseline env or substitutions")
	}
	iso, err := rs.For(config.DeployMapping{Workspace: "Front - TEST", BaselineWorkspace: "Front - DEV"})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if iso == nil {
		t.Fatal("mapping-level baseline must yield a rebinder even when env rebinding is disabled")
	}
}
