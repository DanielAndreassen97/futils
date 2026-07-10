package cmd

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

func TestApplyRefActionOverride(t *testing.T) {
	c := config.Customer{}
	ref := deploy.UnresolvedRef{GUID: "dev-guid", ItemType: "Lakehouse"}
	out := applyRefAction(c, ref, RefAction{Kind: refActionOverride, ItemType: "Lakehouse", ItemName: "LH_Silver"})
	if len(out.ReferenceOverrides) != 1 {
		t.Fatalf("overrides = %#v", out.ReferenceOverrides)
	}
	o := out.ReferenceOverrides[0]
	if o.SourceGUID != "dev-guid" || o.ItemName != "LH_Silver" || o.ItemType != "Lakehouse" {
		t.Errorf("override = %#v", o)
	}
}

func TestApplyRefActionOverrideReplacesExisting(t *testing.T) {
	c := config.Customer{ReferenceOverrides: []config.ReferenceOverride{
		{SourceGUID: "dev-guid", ItemType: "Lakehouse", ItemName: "LH_Old"},
	}}
	ref := deploy.UnresolvedRef{GUID: "dev-guid", ItemType: "Lakehouse"}
	out := applyRefAction(c, ref, RefAction{Kind: refActionOverride, ItemType: "Lakehouse", ItemName: "LH_New"})
	if len(out.ReferenceOverrides) != 1 || out.ReferenceOverrides[0].ItemName != "LH_New" {
		t.Fatalf("expected replacement, got %#v", out.ReferenceOverrides)
	}
}

func TestApplyRefActionIgnore(t *testing.T) {
	c := config.Customer{}
	ref := deploy.UnresolvedRef{GUID: "dev-guid"}
	out := applyRefAction(c, ref, RefAction{Kind: refActionIgnore})
	if !out.IsIgnored("dev-guid") {
		t.Error("expected dev-guid ignored")
	}
	// Idempotent: applying ignore twice doesn't duplicate.
	out = applyRefAction(out, ref, RefAction{Kind: refActionIgnore})
	if len(out.IgnoredReferences) != 1 {
		t.Errorf("ignore not idempotent: %#v", out.IgnoredReferences)
	}
}

func TestApplyRefActionRegister(t *testing.T) {
	c := config.Customer{Environments: []config.Environment{
		{Alias: "TEST", Workspaces: []string{"DW - TEST - Config"}},
	}}
	ref := deploy.UnresolvedRef{GUID: "dev-guid"}
	out := applyRefAction(c, ref, RefAction{Kind: refActionRegister, EnvAlias: "TEST", Workspace: "DW - TEST - Data"})
	ws := out.Environments[0].Workspaces
	if len(ws) != 2 || ws[1] != "DW - TEST - Data" {
		t.Fatalf("register didn't add workspace: %#v", ws)
	}
	// Idempotent: registering the same workspace twice doesn't duplicate.
	out = applyRefAction(out, ref, RefAction{Kind: refActionRegister, EnvAlias: "TEST", Workspace: "DW - TEST - Data"})
	if len(out.Environments[0].Workspaces) != 2 {
		t.Errorf("register not idempotent: %#v", out.Environments[0].Workspaces)
	}
}

func TestRefActionOptionsByReason(t *testing.T) {
	// name-unknown: no "register" suggestion first (we don't know the name to
	// search by) — override/ignore/skip only, and "register" still available.
	nameUnknown := refActionOptions(deploy.UnresolvedRef{GUID: "g", Reason: deploy.ReasonNameUnknown})
	if !containsValue(nameUnknown, refActionOverride) || !containsValue(nameUnknown, refActionIgnore) || !containsValue(nameUnknown, refActionSkip) {
		t.Fatalf("name-unknown options missing core actions: %#v", nameUnknown)
	}
	// not-in-target: "register" should be offered (the item likely lives in an
	// unregistered workspace) and listed first.
	notInTarget := refActionOptions(deploy.UnresolvedRef{GUID: "g", Reason: deploy.ReasonNotInTarget})
	if notInTarget[0].Value != refActionRegister {
		t.Errorf("not-in-target should lead with register, got %#v", notInTarget)
	}
}

func containsValue(opts []ui.MenuOption, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

func TestApplyRefActionSkipNoChange(t *testing.T) {
	c := config.Customer{Environments: []config.Environment{{Alias: "TEST"}}}
	out := applyRefAction(c, deploy.UnresolvedRef{GUID: "g"}, RefAction{Kind: refActionSkip})
	if len(out.ReferenceOverrides) != 0 || len(out.IgnoredReferences) != 0 {
		t.Error("skip should mutate nothing")
	}
}

func TestApplyRefActionRegisterDoesNotMutateInput(t *testing.T) {
	ws := make([]string, 1, 4) // spare capacity → append would mutate in place if aliased
	ws[0] = "DW - TEST - Config"
	c := config.Customer{Environments: []config.Environment{{Alias: "TEST", Workspaces: ws}}}
	_ = applyRefAction(c, deploy.UnresolvedRef{GUID: "g"}, RefAction{Kind: refActionRegister, EnvAlias: "TEST", Workspace: "DW - TEST - Data"})
	if len(c.Environments[0].Workspaces) != 1 || c.Environments[0].Workspaces[0] != "DW - TEST - Config" {
		t.Errorf("applyRefAction register mutated the caller's customer: %#v", c.Environments[0].Workspaces)
	}
}
