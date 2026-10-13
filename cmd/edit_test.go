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

// scriptedPicks answers the phase editor's prompts in order and records what
// each prompt offered, standing in for the interactive checkbox list.
type scriptedPicks struct {
	answers [][]string
	offered [][]string
	initial [][]string
}

func (s *scriptedPicks) pick(_ string, options, initial []string) ([]string, error) {
	s.offered = append(s.offered, options)
	s.initial = append(s.initial, initial)
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

// The phase editor peels phases off the list: what is checked runs in this
// phase, the rest is offered for the next. Checking everything (or nothing)
// ends it with the remainder in the current phase. Phase 1 is never stored.
func TestChoosePostDeployPhases(t *testing.T) {
	runs := []string{"NB_Config", "NB_A", "NB_B", "NB_Final"}
	s := &scriptedPicks{answers: [][]string{{"NB_Config"}, {"NB_A", "NB_B"}, nil}}
	got, err := choosePostDeployPhases(runs, func(string) int { return 1 }, s.pick)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"NB_A": 2, "NB_B": 2, "NB_Final": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(s.offered[1], []string{"NB_A", "NB_B", "NB_Final"}) || !reflect.DeepEqual(s.offered[2], []string{"NB_Final"}) {
		t.Errorf("each phase must offer only the remainder, got %v", s.offered)
	}
}

// Re-opening the editor pre-checks each run in its current phase, so keeping
// the existing setup is just confirming every prompt.
func TestChoosePostDeployPhasesPrechecksCurrent(t *testing.T) {
	runs := []string{"NB_Config", "NB_A"}
	current := map[string]int{"NB_A": 2}
	s := &scriptedPicks{answers: [][]string{{"NB_Config"}, {"NB_A"}}}
	got, err := choosePostDeployPhases(runs, func(n string) int {
		if p := current[n]; p > 1 {
			return p
		}
		return 1
	}, s.pick)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.initial[0], []string{"NB_Config"}) || !reflect.DeepEqual(s.initial[1], []string{"NB_A"}) {
		t.Errorf("initial = %v", s.initial)
	}
	if !reflect.DeepEqual(got, current) {
		t.Errorf("phases = %v, want unchanged %v", got, current)
	}
}

// Everything in one phase stores nothing: no phases map in the config.
func TestChoosePostDeployPhasesAllInOne(t *testing.T) {
	s := &scriptedPicks{answers: [][]string{{"A", "B"}}}
	got, err := choosePostDeployPhases([]string{"A", "B"}, func(string) int { return 1 }, s.pick)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil map", got, err)
	}
}
