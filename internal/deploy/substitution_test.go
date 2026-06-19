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

func TestApplyCustomSubstitutionsItemFilter(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	rb.SetSubstitutions([]Substitution{{FindValue: "X", Literal: "Y", ItemType: "DataPipeline"}})
	// Notebook item shouldn't match a DataPipeline-filtered rule.
	out, outcome := rb.ApplyCustomSubstitutions(LocalItem{Type: "Notebook", DisplayName: "NB"}, "notebook-content.py", []byte("X"))
	if string(out) != "X" || len(outcome.Changes) != 0 {
		t.Errorf("item-type filter not honored: out=%q changes=%#v", out, outcome.Changes)
	}
}
