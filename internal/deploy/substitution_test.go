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
