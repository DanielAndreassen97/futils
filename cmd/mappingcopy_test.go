// Tests that copying deployment mappings from one environment to another
// lands every folder on the target env's own workspace.
//
// Why: a new env (say PROD) is seeded from TEST's mappings with one Enter. If
// the workspace translation is wrong, the first PROD deploy goes to a TEST
// workspace, or to one PROD doesn't own.
//
// Example: TEST maps FrontEnd/Reports → "DW - Rapporter - TEST"; copied to
// PROD it becomes "DW - Rapporter - PROD", and only because PROD lists that
// workspace.
package cmd

import (
	"reflect"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
)

func TestSwapEnvToken_WholeWordOnlyAnyCase(t *testing.T) {
	cases := []struct{ name, from, to, want string }{
		{"DW - Rapporter - TEST", "TEST", "PROD", "DW - Rapporter - PROD"},
		{"DW - Rapporter - test", "TEST", "PROD", "DW - Rapporter - PROD"},
		{"TEST_Contoso", "TEST", "PROD", "PROD_Contoso"},
		{"Fabric Testing Workspace", "TEST", "PROD", "Fabric Testing Workspace"},
		{"DW - Config - COMMON", "TEST", "PROD", "DW - Config - COMMON"},
	}
	for _, c := range cases {
		if got := swapEnvToken(c.name, c.from, c.to); got != c.want {
			t.Errorf("swapEnvToken(%q, %q, %q) = %q, want %q", c.name, c.from, c.to, got, c.want)
		}
	}
}

func TestTranslateMappings_KeepsOnlyWorkspacesTheTargetOwns(t *testing.T) {
	src := []config.DeployMapping{
		{Folder: "BackEnd", Workspace: "DW - Config - TEST", BaselineWorkspace: "DW - Config - DEV"},
		{Folder: "FrontEnd/Reports", Workspace: "DW - Rapporter - TEST"},
		{Folder: "FrontEnd/Extra", Workspace: "DW - Extra - TEST"}, // PROD has no Extra
		{Folder: "Shared", Workspace: "DW - Config - COMMON"},      // shared, registered on PROD
	}
	prod := []string{"DW - Config - PROD", "DW - RAPPORTER - PROD", "DW - Config - COMMON"}

	copied, missing := translateMappings(src, "TEST", "PROD", prod)

	want := []config.DeployMapping{
		{Folder: "BackEnd", Workspace: "DW - Config - PROD", BaselineWorkspace: "DW - Config - DEV"},
		{Folder: "FrontEnd/Reports", Workspace: "DW - RAPPORTER - PROD"}, // the registered spelling
		{Folder: "Shared", Workspace: "DW - Config - COMMON"},
	}
	if !reflect.DeepEqual(copied, want) {
		t.Errorf("copied = %+v\nwant     %+v", copied, want)
	}
	if len(missing) != 1 || missing[0].Folder != "FrontEnd/Extra" {
		t.Errorf("missing = %+v, want only FrontEnd/Extra", missing)
	}
}

func TestMappingCopyChoices_NearestEnvFirst(t *testing.T) {
	m := func(ws string) []config.DeployMapping {
		return []config.DeployMapping{{Folder: "BackEnd", Workspace: ws}}
	}
	customer := config.Customer{Environments: []config.Environment{
		{Alias: "DEV", Workspaces: []string{"DW - DEV"}, Deployments: m("DW - DEV")},
		{Alias: "TEST", Workspaces: []string{"DW - TEST"}, Deployments: m("DW - TEST")},
		{Alias: "PROD", Workspaces: []string{"DW - PROD"}},
		{Alias: "SANDBOX", Workspaces: []string{"DW - SANDBOX"}}, // no mappings: not offered
	}}

	choices := mappingCopyChoices(customer, "PROD")

	var from []string
	for _, c := range choices {
		from = append(from, c.from)
	}
	if !reflect.DeepEqual(from, []string{"TEST", "DEV"}) {
		t.Errorf("order = %v, want [TEST DEV]", from)
	}
	if len(choices[0].copied) != 1 || choices[0].copied[0].Workspace != "DW - PROD" {
		t.Errorf("TEST copy = %+v, want BackEnd → DW - PROD", choices[0].copied)
	}
}
