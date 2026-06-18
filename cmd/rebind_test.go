package cmd

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestBuildRebinderDisabledWithoutBaseline(t *testing.T) {
	customer := config.Customer{
		Environments: []config.Environment{{Alias: "TEST", Workspaces: []string{"DP - TEST - Config"}}},
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
			{Alias: "DEV", Workspaces: []string{"DP - DEV - Config"}},
			{Alias: "TEST", Workspaces: []string{"DP - TEST - Config"}},
		},
	}
	workspaces := []fabric.Workspace{
		{ID: "dev-config", DisplayName: "DP - DEV - Config"},
		{ID: "test-config", DisplayName: "DP - TEST - Config"},
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
		Environments:        []config.Environment{{Alias: "TEST", Workspaces: []string{"DP - TEST - Config"}}},
	}
	if _, err := buildRebinder(&deployFakeAPI{}, "tok", customer, "TEST", nil); err == nil {
		t.Error("expected error when BaselineEnvironment names no known environment")
	}
}
