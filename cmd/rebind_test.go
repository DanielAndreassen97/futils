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
