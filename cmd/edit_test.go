package cmd

import (
	"reflect"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
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
