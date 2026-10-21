package cmd

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

func TestRemoveOverride(t *testing.T) {
	c := config.Customer{ReferenceOverrides: []config.ReferenceOverride{
		{SourceGUID: "a"}, {SourceGUID: "b"},
	}}
	out := removeOverride(c, "a")
	if len(out.ReferenceOverrides) != 1 || out.ReferenceOverrides[0].SourceGUID != "b" {
		t.Fatalf("removeOverride = %#v", out.ReferenceOverrides)
	}
}

func TestRemoveIgnored(t *testing.T) {
	c := config.Customer{IgnoredReferences: []string{"a", "b"}}
	out := removeIgnored(c, "b")
	if len(out.IgnoredReferences) != 1 || out.IgnoredReferences[0] != "a" {
		t.Fatalf("removeIgnored = %#v", out.IgnoredReferences)
	}
}

func TestSetBaseline(t *testing.T) {
	c := config.Customer{Environments: []config.Environment{{Alias: "DEV"}, {Alias: "TEST"}}}
	c = setBaseline(c, "DEV")
	if c.BaselineEnvironment != "DEV" {
		t.Fatalf("BaselineEnvironment = %q, want DEV", c.BaselineEnvironment)
	}
	// Clearing: empty alias resets it.
	c = setBaseline(c, "")
	if c.BaselineEnvironment != "" {
		t.Errorf("expected cleared baseline, got %q", c.BaselineEnvironment)
	}
}

func TestAddSubstitution(t *testing.T) {
	c := config.Customer{}
	c = addSubstitution(c, config.Substitution{FindValue: "a", Literal: "b"})
	if len(c.Substitutions) != 1 || c.Substitutions[0].FindValue != "a" {
		t.Fatalf("addSubstitution = %#v", c.Substitutions)
	}
}

func TestRemoveSubstitution(t *testing.T) {
	c := config.Customer{Substitutions: []config.Substitution{{FindValue: "a"}, {FindValue: "b"}}}
	c = removeSubstitution(c, 0)
	if len(c.Substitutions) != 1 || c.Substitutions[0].FindValue != "b" {
		t.Fatalf("removeSubstitution = %#v", c.Substitutions)
	}
	// out-of-range index is a no-op (defensive)
	c = removeSubstitution(c, 5)
	if len(c.Substitutions) != 1 {
		t.Errorf("out-of-range remove mutated: %#v", c.Substitutions)
	}
}

func TestValidateNewAlias(t *testing.T) {
	existing := []config.Environment{
		{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}},
		{Alias: "PROD", Workspaces: []string{"DW - PROD - Config"}},
	}

	cases := []struct {
		name    string
		alias   string
		wantErr bool
	}{
		{"empty", "", true},
		{"whitespace only", "   ", true},
		{"duplicate exact match", "DEV", true},
		{"duplicate with different case", "dev", true},
		{"new alias", "feature", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNewAlias(tc.alias, existing)
			if (err != nil) != tc.wantErr {
				t.Errorf("alias %q: got err=%v, wantErr=%v", tc.alias, err, tc.wantErr)
			}
		})
	}
}

func TestMergePostDeploySelection(t *testing.T) {
	existing := []string{"NB_Config", "NB_Z_First", "NB_A_Later"}
	// User keeps NB_Z_First and NB_Config, drops NB_A_Later, adds NB_New.
	chosen := []string{"NB_Config", "NB_New", "NB_Z_First"} // picker returns options order (alphabetical)
	got := mergePostDeploySelection(existing, chosen)
	want := []string{"NB_Config", "NB_Z_First", "NB_New"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %v, want %v", got, want)
	}
	if mergePostDeploySelection(existing, nil) != nil {
		t.Fatal("empty selection must return nil")
	}
}

func TestSubstitutionLabel(t *testing.T) {
	cases := []struct {
		in   config.Substitution
		want string
	}{
		{config.Substitution{FindValue: "dev", Literals: map[string]string{"TEST": "test", "PROD": "prod"}},
			`"dev" → PROD: "prod", TEST: "test"`},
		{config.Substitution{FindValue: "x", Literal: "y"}, `"x" → "y"`},
		{config.Substitution{FindValue: "g", TargetType: "Lakehouse", TargetName: "LH_Gold", Attr: "sqlendpoint"},
			`"g" → Lakehouse "LH_Gold".sqlendpoint`},
	}
	for _, c := range cases {
		if got := substitutionLabel(c.in); got != c.want {
			t.Errorf("substitutionLabel(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMappingLabel(t *testing.T) {
	cases := []struct{ folder, repo, want string }{
		{"Backend", "", "Backend/"},
		{"", "", "(repo root)"},
		{"", "/Users/x/GIT/dataplatform-frontend", "dataplatform-frontend"},
		{"Reports", "/Users/x/GIT/dataplatform-frontend", "dataplatform-frontend/Reports/"},
	}
	for _, c := range cases {
		if got := mappingLabel(c.folder, c.repo); got != c.want {
			t.Errorf("mappingLabel(%q, %q) = %q, want %q", c.folder, c.repo, got, c.want)
		}
	}
}

// The editor's phases become the stored map: gaps close up (1 and 3 -> 1 and
// 2, so no phase is ever empty) and phase 1 is never stored.
func TestCompactPhases(t *testing.T) {
	names := []string{"NB_Config", "NB_A", "NB_B", "NB_Final"}
	got := compactPhases(names, []int{1, 3, 3, 7})
	want := map[string]int{"NB_A": 2, "NB_B": 2, "NB_Final": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	// Everything in one phase, whatever its number, stores nothing.
	if got := compactPhases([]string{"A", "B"}, []int{4, 4}); got != nil {
		t.Fatalf("single phase = %v, want nil", got)
	}
}

// Switching post-deploy runs to parallel opens the run-order editor. Backing
// out of it must back out of the switch: parallel with no run order runs every
// notebook at once, including ones that depend on each other.
func TestTogglePostDeployModeEscKeepsSequential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Customers: map[string]config.Customer{
		"Fabrikam": {PostDeployRuns: []string{"NB_Config", "NB_Load_A", "NB_Load_B"}},
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	origPick, origEdit := postDeployModePicker, postDeployPhaseEditor
	t.Cleanup(func() { postDeployModePicker, postDeployPhaseEditor = origPick, origEdit })
	postDeployModePicker = func(string, []ui.MenuOption) (string, error) { return "parallel", nil }

	postDeployPhaseEditor = func(string, []string, []int) ([]int, error) { return nil, ui.ErrGoBack }
	captureStdout(t, func() {
		if err := togglePostDeployMode(path, "Fabrikam"); err != nil {
			t.Fatal(err)
		}
	})
	got, _ := config.Load(path)
	if c := got.Customers["Fabrikam"]; c.ParallelPostDeploy {
		t.Fatalf("esc in the run-order editor must leave the mode sequential: %+v", c)
	}

	postDeployPhaseEditor = func(string, []string, []int) ([]int, error) { return []int{1, 2, 2}, nil }
	captureStdout(t, func() {
		if err := togglePostDeployMode(path, "Fabrikam"); err != nil {
			t.Fatal(err)
		}
	})
	got, _ = config.Load(path)
	c := got.Customers["Fabrikam"]
	if !c.ParallelPostDeploy || c.PostDeployPhase("NB_Load_A") != 2 || c.PostDeployPhase("NB_Config") != 1 {
		t.Fatalf("confirmed editor must save parallel with its run order: %+v", c)
	}
}

// A workspace is either common or on an environment, never both: a common
// workspace on DEV would make its references rebind by name in one deploy and
// stay as-is in the next. So the "add workspace" pickers hide common ones, and
// the common picker hides env ones.
func TestAvailableWorkspaceNamesHidesCommonAndEnvWorkspaces(t *testing.T) {
	api := newDemoClient()
	customer := demoCustomer("/repo")
	customer.CommonWorkspaces = []string{demoIngestWS}
	names, err := availableWorkspaceNames(api, "tok", customer)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{demoIngestWS, demoConfigWS("DEV")} {
		if slices.Contains(names, hidden) {
			t.Errorf("%q must not be offered: %v", hidden, names)
		}
	}
	if !slices.Contains(names, demoRawWS("DEV")) {
		t.Errorf("an unattached workspace must be offered: %v", names)
	}
	// A deploy-target workspace that isn't in the env's own list stays on
	// offer: adding it as a reference workspace is a normal step.
	customer.Environments[1].Workspaces = nil
	names, err = availableWorkspaceNames(api, "tok", customer)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, demoConfigWS("TEST")) {
		t.Errorf("a mapping target missing from the env's list must still be offered: %v", names)
	}
}
