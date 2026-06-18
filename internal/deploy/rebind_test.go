package deploy

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// DEV-baseline GUIDs baked into the git notebook.
const (
	devConfigLH   = "11111111-1111-1111-1111-111111111111"
	devConfigWS   = "22222222-2222-2222-2222-222222222222"
	devSilverLH   = "33333333-3333-3333-3333-333333333333"
)

func rebindNotebook(defaultLH, defaultWS, defaultName, knownID string) []byte {
	return []byte(`# Fabric notebook source

# METADATA ********************

# META {
# META   "dependencies": {
# META     "lakehouse": {
# META       "default_lakehouse": "` + defaultLH + `",
# META       "default_lakehouse_name": "` + defaultName + `",
# META       "default_lakehouse_workspace_id": "` + defaultWS + `",
# META       "known_lakehouses": [
# META         { "id": "` + knownID + `" }
# META       ]
# META     }
# META   }
# META }
`)
}

// newRebindFixture wires a fake with both envs. Baseline (DEV) holds the GUIDs
// committed in git; target (TEST) holds the same names under new GUIDs.
func newRebindFixture(t *testing.T, overrides map[string]Override) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DP - DEV - Config"},
			{ID: "dev-data", DisplayName: "DP - DEV - Data"},
			{ID: "test-config", DisplayName: "DP - TEST - Config"},
			{ID: "test-data", DisplayName: "DP - TEST - Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-config": {{ID: devConfigLH, DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"dev-data":   {{ID: devSilverLH, DisplayName: "LH_Silver", Type: "Lakehouse"}},
			"test-config": {{ID: "test-config-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"test-data":   {{ID: "test-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}},
		},
	}
	baselineWS := []fabric.Workspace{f.workspaces[0], f.workspaces[1]}
	targetWS := []fabric.Workspace{f.workspaces[2], f.workspaces[3]}
	rb, err := NewRebinder(f, "tok", baselineWS, targetWS, overrides)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

func TestRebindDefaultLakehouseByName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, outcome := rb.RebindNotebookLakehouses(in)
	s := string(out)
	if !strings.Contains(s, "test-config-lh") {
		t.Errorf("default_lakehouse not rebound to target GUID:\n%s", s)
	}
	if !strings.Contains(s, "\"test-config\"") || strings.Contains(s, devConfigWS) {
		t.Errorf("default_lakehouse_workspace_id not rebound to target workspace:\n%s", s)
	}
	if strings.Contains(s, devConfigLH) {
		t.Errorf("baseline default_lakehouse GUID still present:\n%s", s)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("expected no unresolved refs, got %#v", outcome.Unresolved)
	}
}

func TestRebindKnownLakehouseViaBaselineName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, _ := rb.RebindNotebookLakehouses(in)
	s := string(out)
	if !strings.Contains(s, "test-silver-lh") {
		t.Errorf("known_lakehouse LH_Silver not rebound:\n%s", s)
	}
	if strings.Contains(s, devSilverLH) {
		t.Errorf("baseline known_lakehouse GUID still present:\n%s", s)
	}
}

func TestRebindUnresolvedKnownLakehouse(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// A known lakehouse GUID that exists in NEITHER env -> unresolved, untouched.
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if !strings.Contains(string(out), unknown) {
		t.Error("unresolved GUID should be left unchanged in content")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].GUID != unknown || outcome.Unresolved[0].Location != "known_lakehouses" {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}

func TestRebindOverrideTakesPrecedence(t *testing.T) {
	// Override maps the unknown baseline GUID directly to LH_Silver by name; add
	// LH_Silver to the target so the override resolves.
	overrides := map[string]Override{
		"99999999-9999-9999-9999-999999999999": {ItemType: "Lakehouse", ItemName: "LH_Silver"},
	}
	rb := newRebindFixture(t, overrides)
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("override should resolve the GUID, got unresolved %#v", outcome.Unresolved)
	}
	if !strings.Contains(string(out), "test-silver-lh") || strings.Contains(string(out), unknown) {
		t.Errorf("override not applied:\n%s", string(out))
	}
}

func TestRebindOverrideOnDefaultLakehouse(t *testing.T) {
	// Override the DEV default-lakehouse GUID directly to a different target
	// lakehouse by name — proves the override beats the stored
	// default_lakehouse_name zero-config path.
	overrides := map[string]Override{
		devConfigLH: {ItemType: "Lakehouse", ItemName: "LH_Silver"},
	}
	rb := newRebindFixture(t, overrides)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	out, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("expected no unresolved, got %#v", outcome.Unresolved)
	}
	s := string(out)
	if !strings.Contains(s, "test-silver-lh") {
		t.Errorf("override on default_lakehouse not applied (expected test-silver-lh):\n%s", s)
	}
	if strings.Contains(s, devConfigLH) {
		t.Errorf("baseline default_lakehouse GUID still present:\n%s", s)
	}
}

func TestUnresolvedCarriesReason(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// A known lakehouse GUID that exists in NEITHER env → name-unknown.
	unknown := "99999999-9999-9999-9999-999999999999"
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Reason != ReasonNameUnknown {
		t.Fatalf("unresolved = %#v (want one with ReasonNameUnknown)", outcome.Unresolved)
	}
}

func TestUnresolvedReasonNotInTarget(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// default_lakehouse_name that the baseline has but the target lacks: use a
	// name absent from the target env. Build a notebook whose default name is
	// "LH_Ghost" (not in target) — name path → not-in-target.
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_Ghost", "")
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Reason != ReasonNotInTarget {
		t.Fatalf("unresolved = %#v (want one with ReasonNotInTarget)", outcome.Unresolved)
	}
}

func TestRebindNonNotebookUnchanged(t *testing.T) {
	rb := newRebindFixture(t, nil)
	plain := []byte("table Foo\ncolumn Bar\n")
	out, outcome := rb.RebindNotebookLakehouses(plain)
	if string(out) != string(plain) || len(outcome.Unresolved) != 0 {
		t.Errorf("non-notebook content should pass through unchanged")
	}
}

func TestRebindNotebookReportsChanges(t *testing.T) {
	rb := newRebindFixture(t, nil)
	in := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	_, outcome := rb.RebindNotebookLakehouses(in)
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", outcome.Unresolved)
	}
	// Expect three changes: default lakehouse (Lakehouse), its workspace (Workspace),
	// and the known lakehouse (Lakehouse). Deduped by Old.
	kinds := map[string]int{}
	for _, c := range outcome.Changes {
		kinds[c.Kind]++
		if c.Old == "" || c.New == "" || c.Old == c.New {
			t.Errorf("bad change %#v", c)
		}
	}
	if kinds["Lakehouse"] != 2 || kinds["Workspace"] != 1 {
		t.Fatalf("change kinds = %#v (want Lakehouse:2 Workspace:1)", kinds)
	}
	// The default-lakehouse change must map the DEV GUID to the TEST GUID.
	var found bool
	for _, c := range outcome.Changes {
		if c.Old == devConfigLH && c.New == "test-config-lh" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing default_lakehouse change %s→test-config-lh in %#v", devConfigLH, outcome.Changes)
	}
}

func TestDefaultLakehouseNoNameUnknownGUIDReason(t *testing.T) {
	rb := newRebindFixture(t, nil)
	unknown := "88888888-8888-8888-8888-888888888888"
	// No default_lakehouse_name, GUID not in baseline → name-unknown, not not-in-target.
	in := rebindNotebook(unknown, devConfigWS, "", devSilverLH)
	_, outcome := rb.RebindNotebookLakehouses(in)
	var dl *UnresolvedRef
	for i := range outcome.Unresolved {
		if outcome.Unresolved[i].Location == "default_lakehouse" {
			dl = &outcome.Unresolved[i]
		}
	}
	if dl == nil {
		t.Fatalf("expected a default_lakehouse unresolved, got %#v", outcome.Unresolved)
	}
	if dl.Reason != ReasonNameUnknown {
		t.Errorf("default_lakehouse Reason = %q, want %q", dl.Reason, ReasonNameUnknown)
	}
}
