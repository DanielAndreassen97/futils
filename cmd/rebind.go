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
// BaselineEnvironment set — rebinding is then disabled and the rest of the
// deploy proceeds unchanged.
func buildRebinder(client deploy.FabricClient, token string, customer config.Customer, targetAlias string, workspaces []fabric.Workspace) (*deploy.Rebinder, error) {
	if customer.BaselineEnvironment == "" {
		return nil, nil
	}
	baselineEnv, ok := customer.EnvironmentByAlias(customer.BaselineEnvironment)
	if !ok {
		return nil, fmt.Errorf("baseline environment %q is not one of the customer's environments", customer.BaselineEnvironment)
	}
	targetEnv, ok := customer.EnvironmentByAlias(targetAlias)
	if !ok {
		return nil, fmt.Errorf("target environment %q not found", targetAlias)
	}

	baselineWS, err := resolveWorkspaceSet(workspaces, baselineEnv.AllWorkspaces())
	if err != nil {
		return nil, fmt.Errorf("baseline env %q: %w", customer.BaselineEnvironment, err)
	}
	targetWS, err := resolveWorkspaceSet(workspaces, targetEnv.AllWorkspaces())
	if err != nil {
		return nil, fmt.Errorf("target env %q: %w", targetAlias, err)
	}

	overrides := make(map[string]deploy.Override, len(customer.ReferenceOverrides))
	for _, o := range customer.ReferenceOverrides {
		overrides[o.SourceGUID] = deploy.Override{ItemType: o.ItemType, ItemName: o.ItemName}
	}
	return deploy.NewRebinder(client, token, baselineWS, targetWS, overrides)
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
