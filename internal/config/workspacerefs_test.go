package config

import (
	"reflect"
	"testing"
)

// refFixture is a two-customer config that holds the same workspace name in all
// three places a workspace name can appear: an environment's workspace list, a
// deploy mapping's target, and a mapping's baseline override.
func refFixture() Config {
	return Config{Customers: map[string]Customer{
		"Fabrikam": {
			Environments: []Environment{
				{
					Alias:      "DEV",
					Workspaces: []string{"DW - Finance", "DW - SemMod"},
				},
				{
					Alias:      "TEST",
					Workspaces: []string{"DW - SemMod"},
					Deployments: []DeployMapping{
						{Folder: "Backend", Workspace: "DW - Finance"},
						{Folder: "Frontend", Workspace: "DW - SemMod"},
					},
				},
			},
		},
		"Contoso": {
			Environments: []Environment{
				{
					Alias:      "PROD",
					Workspaces: []string{"CT - Core"},
					Deployments: []DeployMapping{
						{Folder: "Shared", Workspace: "CT - Core", BaselineWorkspace: "DW - Finance"},
					},
				},
			},
		},
	}}
}

func TestFindWorkspaceRefsCoversAllThreeKinds(t *testing.T) {
	got := FindWorkspaceRefs(refFixture(), "DW - Finance")

	want := []WorkspaceRef{
		{Customer: "Contoso", Environment: "PROD", Kind: RefDeployBaseline, Mapping: "Shared"},
		{Customer: "Fabrikam", Environment: "DEV", Kind: RefEnvWorkspace},
		{Customer: "Fabrikam", Environment: "TEST", Kind: RefDeployTarget, Mapping: "Backend"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindWorkspaceRefs =\n %+v\nwant\n %+v", got, want)
	}
}

func TestFindWorkspaceRefsIsSortedByCustomer(t *testing.T) {
	// Config.Customers is a map, so without an explicit sort the printed
	// impact list would shuffle between runs.
	for i := 0; i < 20; i++ {
		got := FindWorkspaceRefs(refFixture(), "DW - SemMod")
		if len(got) != 3 || got[0].Customer != "Fabrikam" || got[0].Environment != "DEV" {
			t.Fatalf("unstable or wrong order on run %d: %+v", i, got)
		}
	}
}

func TestFindWorkspaceRefsNoMatch(t *testing.T) {
	if got := FindWorkspaceRefs(refFixture(), "Nope"); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestFindWorkspaceRefsIsCaseSensitive(t *testing.T) {
	// authAndResolveWorkspaces resolves names with an exact map lookup, so a
	// case-insensitive scan here would claim to repair references that were
	// never wired up in the first place.
	if got := FindWorkspaceRefs(refFixture(), "dw - finance"); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestRenameWorkspaceRefsRewritesAllThreeKinds(t *testing.T) {
	cfg := refFixture()

	n := RenameWorkspaceRefs(&cfg, "DW - Finance", "DW - Finance PROD")
	if n != 3 {
		t.Errorf("renamed %d references, want 3", n)
	}

	fab := cfg.Customers["Fabrikam"]
	if got := fab.Environments[0].Workspaces; !reflect.DeepEqual(got, []string{"DW - Finance PROD", "DW - SemMod"}) {
		t.Errorf("DEV workspaces = %v", got)
	}
	if got := fab.Environments[1].Deployments[0].Workspace; got != "DW - Finance PROD" {
		t.Errorf("deploy target = %q", got)
	}
	con := cfg.Customers["Contoso"]
	if got := con.Environments[0].Deployments[0].BaselineWorkspace; got != "DW - Finance PROD" {
		t.Errorf("baseline = %q", got)
	}
	// Untouched names stay put.
	if got := con.Environments[0].Deployments[0].Workspace; got != "CT - Core" {
		t.Errorf("unrelated target changed to %q", got)
	}
}

func TestRenameWorkspaceRefsNoMatchLeavesConfigUntouched(t *testing.T) {
	cfg := refFixture()
	before := refFixture()

	if n := RenameWorkspaceRefs(&cfg, "Nope", "Whatever"); n != 0 {
		t.Errorf("renamed %d, want 0", n)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Error("config was modified despite no matches")
	}
}

func TestRemoveWorkspaceRefsDropsNameFromEnvironment(t *testing.T) {
	cfg := refFixture()

	n := RemoveWorkspaceRefs(&cfg, "DW - Finance")
	if n != 3 {
		t.Errorf("removed %d references, want 3", n)
	}

	fab := cfg.Customers["Fabrikam"]
	if got := fab.Environments[0].Workspaces; !reflect.DeepEqual(got, []string{"DW - SemMod"}) {
		t.Errorf("DEV workspaces = %v", got)
	}
}

func TestRemoveWorkspaceRefsDropsDeployMappingButKeepsSiblings(t *testing.T) {
	cfg := refFixture()

	RemoveWorkspaceRefs(&cfg, "DW - Finance")

	deps := cfg.Customers["Fabrikam"].Environments[1].Deployments
	if len(deps) != 1 || deps[0].Folder != "Frontend" {
		t.Errorf("deployments = %+v, want only the Frontend mapping", deps)
	}
}

func TestRemoveWorkspaceRefsClearsBaselineButKeepsMapping(t *testing.T) {
	// A mapping without a baseline override falls back to the customer-level
	// baseline environment, so it stays useful. A mapping without a target does
	// not, which is why the two kinds are removed differently.
	cfg := refFixture()

	RemoveWorkspaceRefs(&cfg, "DW - Finance")

	deps := cfg.Customers["Contoso"].Environments[0].Deployments
	if len(deps) != 1 {
		t.Fatalf("deployments = %+v, want the mapping kept", deps)
	}
	if deps[0].BaselineWorkspace != "" {
		t.Errorf("baseline = %q, want cleared", deps[0].BaselineWorkspace)
	}
	if deps[0].Workspace != "CT - Core" {
		t.Errorf("target = %q, want untouched", deps[0].Workspace)
	}
}

func TestRemoveWorkspaceRefsKeepsEmptiedEnvironment(t *testing.T) {
	// A customer with zero workspaces in an environment is a valid config state
	// (see Customer's doc comment) — dropping the environment would silently
	// throw away its deploy mappings and alias.
	cfg := Config{Customers: map[string]Customer{
		"Fabrikam": {Environments: []Environment{{Alias: "DEV", Workspaces: []string{"Only one"}}}},
	}}

	if n := RemoveWorkspaceRefs(&cfg, "Only one"); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	envs := cfg.Customers["Fabrikam"].Environments
	if len(envs) != 1 || envs[0].Alias != "DEV" {
		t.Fatalf("environments = %+v, want DEV kept", envs)
	}
	if len(envs[0].Workspaces) != 0 {
		t.Errorf("workspaces = %v, want empty", envs[0].Workspaces)
	}
}

func TestRemoveWorkspaceRefsNoMatchLeavesConfigUntouched(t *testing.T) {
	cfg := refFixture()
	before := refFixture()

	if n := RemoveWorkspaceRefs(&cfg, "Nope"); n != 0 {
		t.Errorf("removed %d, want 0", n)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Error("config was modified despite no matches")
	}
}

func TestWorkspaceRefKindString(t *testing.T) {
	cases := map[WorkspaceRefKind]string{
		RefEnvWorkspace:   "workspace",
		RefDeployTarget:   "deploy mapping",
		RefDeployBaseline: "baseline workspace",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", kind, got, want)
		}
	}
}

func TestRemoveWorkspaceRefsCountsABothMatchOnce(t *testing.T) {
	// A mapping that both deploys TO the workspace and names it as its baseline
	// is one reference going away, not two: the mapping is dropped, so its
	// baseline never needed clearing.
	cfg := Config{Customers: map[string]Customer{
		"Fabrikam": {Environments: []Environment{{
			Alias: "DEV",
			Deployments: []DeployMapping{
				{Folder: "Backend", Workspace: "DW - Finance", BaselineWorkspace: "DW - Finance"},
			},
		}}},
	}}

	if n := RemoveWorkspaceRefs(&cfg, "DW - Finance"); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if len(cfg.Customers["Fabrikam"].Environments[0].Deployments) != 0 {
		t.Error("the mapping should have been dropped")
	}
}
