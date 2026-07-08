package deploy

import "testing"

func TestResolveTargetAttr(t *testing.T) {
	rb := newSemmodSQLRebinder(t) // from semmod_test.go: target has LH_ConfigLog=test-lh with sqlByLH test-lh={testhost...,test-ep-id}
	// id
	if v, ok := rb.ResolveTargetAttr("Lakehouse", "LH_ConfigLog", "id"); !ok || v != "test-lh" {
		t.Errorf("id = %q ok=%v", v, ok)
	}
	// sqlendpoint host
	if v, ok := rb.ResolveTargetAttr("Lakehouse", "LH_ConfigLog", "sqlendpoint"); !ok || v != "testhost.datawarehouse.fabric.microsoft.com" {
		t.Errorf("sqlendpoint = %q ok=%v", v, ok)
	}
	// sqlendpointid
	if v, ok := rb.ResolveTargetAttr("Lakehouse", "LH_ConfigLog", "sqlendpointid"); !ok || v != "test-ep-id" {
		t.Errorf("sqlendpointid = %q ok=%v", v, ok)
	}
	// empty attr defaults to id
	if v, ok := rb.ResolveTargetAttr("Lakehouse", "LH_ConfigLog", ""); !ok || v != "test-lh" {
		t.Errorf("empty attr = %q ok=%v", v, ok)
	}
	// miss: unknown name
	if _, ok := rb.ResolveTargetAttr("Lakehouse", "LH_Ghost", "id"); ok {
		t.Error("expected miss for unknown name")
	}
}

func TestSetSubstitutions(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: "x", Literal: "y"}})
	if len(rb.substitutions) != 1 || rb.substitutions[0].Literal != "y" {
		t.Fatalf("substitutions = %#v", rb.substitutions)
	}
}

func TestApplyCustomSubstitutionsLiteral(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: "OLD_TOKEN", Literal: "NEW_TOKEN"}})
	item := LocalItem{Type: "Notebook", DisplayName: "NB"}
	out, outcome := rb.ApplyCustomSubstitutions(item, "notebook-content.py", []byte("x = 'OLD_TOKEN'"))
	if string(out) != "x = 'NEW_TOKEN'" {
		t.Errorf("literal sub not applied: %q", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Kind != "Substitution" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestApplyCustomSubstitutionsResolved(t *testing.T) {
	rb := newSemmodSQLRebinder(t) // target LH_ConfigLog → test-lh
	rb.SetSubstitutions([]Substitution{{FindValue: "DEV_LH_GUID", TargetType: "Lakehouse", TargetName: "LH_ConfigLog", Attr: "id"}})
	out, outcome := rb.ApplyCustomSubstitutions(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte(`lh = "DEV_LH_GUID"`))
	if string(out) != `lh = "test-lh"` {
		t.Errorf("resolved sub not applied: %q", out)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].New != "test-lh" {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestApplyCustomSubstitutionsUnresolved(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: "FIND", TargetType: "Lakehouse", TargetName: "LH_Ghost", Attr: "id"}})
	out, outcome := rb.ApplyCustomSubstitutions(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte("FIND"))
	if string(out) != "FIND" {
		t.Error("unresolved sub should leave content unchanged")
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Location != "custom substitution" {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}

func TestApplyCustomSubstitutionsRegexRecordsConcreteOld(t *testing.T) {
	// A regex substitution must record the concrete matched value in RebindChange.Old,
	// not the raw pattern string. Before the fix, Old is set to sub.FindValue
	// (the pattern "cfg-[0-9]+") regardless of what was actually matched.
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: `cfg-[0-9]+`, Literal: "cfg-prod", IsRegex: true}})
	item := LocalItem{Type: "Notebook", DisplayName: "NB"}
	content := []byte(`connection = "cfg-123"`)
	_, outcome := rb.ApplyCustomSubstitutions(item, "notebook-content.py", content)
	if len(outcome.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %#v", len(outcome.Changes), outcome.Changes)
	}
	ch := outcome.Changes[0]
	if ch.Old == `cfg-[0-9]+` {
		t.Errorf("RebindChange.Old = regex pattern %q — must be the concrete matched value", ch.Old)
	}
	if ch.Old != "cfg-123" {
		t.Errorf("RebindChange.Old = %q, want %q (the concrete matched value)", ch.Old, "cfg-123")
	}
}

// TestSetSubstitutionsCompilesRegex asserts that SetSubstitutions pre-compiles
// regex rules and stores the result so ApplyCustomSubstitutions can reuse it.
func TestSetSubstitutionsCompilesRegex(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	subs := []Substitution{
		{FindValue: `(\d+)-dev`, Literal: "$1-prod", IsRegex: true},
		{FindValue: "literal-find", Literal: "literal-repl", IsRegex: false},
	}
	rb.SetSubstitutions(subs)
	// The regex rule must have a compiled regexp stored; the literal rule must not.
	if rb.substitutions[0].compiled == nil {
		t.Error("regex rule: compiled must be non-nil after SetSubstitutions")
	}
	if rb.substitutions[1].compiled != nil {
		t.Error("literal rule: compiled should remain nil")
	}
}

// TestApplyCustomSubstitutionsRegexExpandedNew asserts that the RebindChange.New
// for a regex rule records the EXPANDED replacement (the concrete text written),
// not the raw template string.
//
// Rule: find=`(\d+)-dev`  repl=`$1-prod`
// Content: "conn-42-dev"  → matched "42-dev", written "42-prod"
// Before fix: New == "$1-prod"  (raw template — RED)
// After fix:  New == "42-prod" (expanded — GREEN)
func TestApplyCustomSubstitutionsRegexExpandedNew(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{
		{FindValue: `(\d+)-dev`, Literal: "$1-prod", IsRegex: true},
	})
	item := LocalItem{Type: "Notebook", DisplayName: "NB"}
	content := []byte("conn-42-dev")
	out, outcome := rb.ApplyCustomSubstitutions(item, "notebook-content.py", content)

	// Content must be correctly replaced.
	if string(out) != "conn-42-prod" {
		t.Errorf("content: got %q, want %q", string(out), "conn-42-prod")
	}
	if len(outcome.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %#v", len(outcome.Changes), outcome.Changes)
	}
	ch := outcome.Changes[0]
	// Old must be the concrete matched substring.
	if ch.Old != "42-dev" {
		t.Errorf("RebindChange.Old = %q, want %q", ch.Old, "42-dev")
	}
	// New must be the expanded replacement, not the raw template.
	if ch.New == "$1-prod" {
		t.Errorf("RebindChange.New = raw template %q — must be the concrete expanded value", ch.New)
	}
	if ch.New != "42-prod" {
		t.Errorf("RebindChange.New = %q, want %q", ch.New, "42-prod")
	}
}

func TestApplyCustomSubstitutionsItemFilter(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: "X", Literal: "Y", ItemType: "DataPipeline"}})
	// Notebook item shouldn't match a DataPipeline-filtered rule.
	out, outcome := rb.ApplyCustomSubstitutions(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte("X"))
	if string(out) != "X" || len(outcome.Changes) != 0 {
		t.Errorf("item-type filter not honored: out=%q changes=%#v", out, outcome.Changes)
	}
}
