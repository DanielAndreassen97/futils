package cmd

import (
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// buildRebinder constructs a deploy.Rebinder for a deploy run: it resolves the
// baseline environment's and the target environment's full workspace sets to
// fabric.Workspace structs, converts the customer's reference overrides into the
// engine's own Override type (so internal/deploy never imports internal/config),
// and builds the two name indices. Returns (nil, nil) when the customer has no
// BaselineEnvironment set AND no custom substitutions — rebinding is then
// disabled entirely. When substitutions are present but no baseline is set the
// rebinder is built with an empty baseline workspace set (auto-rebind no-ops,
// but custom substitutions still apply).
func buildRebinder(client deploy.FabricClient, token string, customer config.Customer, targetAlias string, workspaces []fabric.Workspace) (*deploy.Rebinder, error) {
	hasSubs := len(customer.Substitutions) > 0
	if customer.BaselineEnvironment == "" && !hasSubs {
		return nil, nil
	}

	var baselineWS []fabric.Workspace
	if customer.BaselineEnvironment != "" {
		baselineEnv, ok := customer.EnvironmentByAlias(customer.BaselineEnvironment)
		if !ok {
			return nil, fmt.Errorf("baseline environment %q is not one of the customer's environments", customer.BaselineEnvironment)
		}
		var err error
		baselineWS, err = resolveWorkspaceSet(workspaces, baselineEnv.AllWorkspaces())
		if err != nil {
			return nil, fmt.Errorf("baseline env %q: %w", customer.BaselineEnvironment, err)
		}
	}

	targetEnv, ok := customer.EnvironmentByAlias(targetAlias)
	if !ok {
		return nil, fmt.Errorf("target environment %q not found", targetAlias)
	}
	targetWS, err := resolveWorkspaceSet(workspaces, targetEnv.AllWorkspaces())
	if err != nil {
		return nil, fmt.Errorf("target env %q: %w", targetAlias, err)
	}

	rb, err := deploy.NewRebinder(client, token, baselineWS, targetWS, overridesFromConfig(customer))
	if err != nil {
		return nil, err
	}
	rb.SetSubstitutions(toDeploySubstitutions(customer.Substitutions))
	return rb, nil
}

// overridesFromConfig converts the customer's reference overrides into the
// engine's config-free Override map, keyed by the baseline GUID (or literal
// name, for name-form references).
func overridesFromConfig(customer config.Customer) map[string]deploy.Override {
	overrides := make(map[string]deploy.Override, len(customer.ReferenceOverrides))
	for _, o := range customer.ReferenceOverrides {
		overrides[o.SourceGUID] = deploy.Override{ItemType: o.ItemType, ItemName: o.ItemName}
	}
	return overrides
}

// rebinderSet hands out the right rebinder for each deploy mapping: the shared
// env-level one (baseline env span → target env span) for mappings without a
// BaselineWorkspace, and an isolated, cached one (mapping's baseline workspace →
// the mapping's own deploy workspace) when the mapping sets one. Isolation is
// the point: a frontend repo's baked GUIDs resolve only against the frontend
// workspace pair, so same-named backend items can't collide or go ambiguous.
// Reference overrides and custom substitutions are shared across all rebinders.
type rebinderSet struct {
	client     deploy.FabricClient
	token      string
	customer   config.Customer
	workspaces []fabric.Workspace
	shared     *deploy.Rebinder // env-level; nil when rebinding is disabled
	cache      map[string]*deploy.Rebinder
}

// newRebinderSet builds the shared env-level rebinder eagerly (same semantics
// as buildRebinder, including the nil-when-disabled case) and defers isolated
// per-mapping rebinders until a mapping actually needs one.
func newRebinderSet(client deploy.FabricClient, token string, customer config.Customer, targetAlias string, workspaces []fabric.Workspace) (*rebinderSet, error) {
	shared, err := buildRebinder(client, token, customer, targetAlias, workspaces)
	if err != nil {
		return nil, err
	}
	return &rebinderSet{
		client: client, token: token, customer: customer, workspaces: workspaces,
		shared: shared, cache: map[string]*deploy.Rebinder{},
	}, nil
}

// For returns the rebinder for one mapping. A mapping with a BaselineWorkspace
// gets its isolated rebinder even when env-level rebinding is disabled — the
// mapping's own baseline is authoritative for it. A nil set (rebinding fully
// disabled and never configured) yields nil for every mapping.
func (rs *rebinderSet) For(m config.DeployMapping) (*deploy.Rebinder, error) {
	if rs == nil {
		return nil, nil
	}
	if m.BaselineWorkspace == "" {
		return rs.shared, nil
	}
	key := m.BaselineWorkspace + "\x00" + m.Workspace
	if rb, ok := rs.cache[key]; ok {
		return rb, nil
	}
	baseWS, err := resolveWorkspaceSet(rs.workspaces, []string{m.BaselineWorkspace})
	if err != nil {
		return nil, fmt.Errorf("mapping baseline workspace %q: %w", m.BaselineWorkspace, err)
	}
	targetWS, err := resolveWorkspaceSet(rs.workspaces, []string{m.Workspace})
	if err != nil {
		return nil, fmt.Errorf("mapping target workspace %q: %w", m.Workspace, err)
	}
	rb, err := deploy.NewRebinder(rs.client, rs.token, baseWS, targetWS, overridesFromConfig(rs.customer))
	if err != nil {
		return nil, err
	}
	rb.SetSubstitutions(toDeploySubstitutions(rs.customer.Substitutions))
	rs.cache[key] = rb
	return rb, nil
}

// toDeploySubstitutions converts config substitution rules into the engine's
// config-free mirror.
func toDeploySubstitutions(subs []config.Substitution) []deploy.Substitution {
	out := make([]deploy.Substitution, len(subs))
	for i, s := range subs {
		out[i] = deploy.Substitution{
			FindValue: s.FindValue, IsRegex: s.IsRegex,
			ItemType: s.ItemType, ItemName: s.ItemName, FilePath: s.FilePath,
			TargetType: s.TargetType, TargetName: s.TargetName, Attr: s.Attr, Literal: s.Literal,
		}
	}
	return out
}

// resolveWorkspaceSet maps a list of workspace display names to the
// fabric.Workspace structs the user can see, erroring on the first name that
// isn't visible (a config referencing a workspace the user can't access).
func resolveWorkspaceSet(visible []fabric.Workspace, names []string) ([]fabric.Workspace, error) {
	out := make([]fabric.Workspace, 0, len(names))
	for _, n := range names {
		ws, err := resolveWorkspaceByName(visible, n)
		if err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, nil
}
