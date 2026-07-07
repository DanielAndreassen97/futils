package cmd

import (
	"github.com/DanielAndreassen97/futils/internal/deploy"
)

// postDeployRun identifies one notebook to run after a deploy: the deployed
// item and the workspace it landed in.
type postDeployRun struct {
	Name          string
	ItemID        string
	WorkspaceID   string
	WorkspaceName string
}

// postDeployCandidates intersects the customer's registered post-deploy
// notebooks with what THIS deploy actually published: successful (Err == nil)
// create/update results of type Notebook whose name is registered and whose
// deployed item ID is known. Order follows the registered list (config order
// = run order); a name deployed to several workspaces yields one run per
// workspace, in results order. Returns nil when there is nothing to offer.
func postDeployCandidates(registered []string, results []deploy.Result, wsNames map[string]string) []postDeployRun {
	if len(registered) == 0 || len(results) == 0 {
		return nil
	}
	byName := map[string][]deploy.Result{}
	for _, r := range results {
		if r.Err != nil || r.Action == deploy.ActionDelete || r.Type != "Notebook" || r.ID == "" {
			continue
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	var runs []postDeployRun
	seen := map[string]bool{}
	for _, name := range registered {
		for _, r := range byName[name] {
			key := name + "\x00" + r.WorkspaceID
			if seen[key] {
				continue
			}
			seen[key] = true
			runs = append(runs, postDeployRun{
				Name:          name,
				ItemID:        r.ID,
				WorkspaceID:   r.WorkspaceID,
				WorkspaceName: wsNames[r.WorkspaceID],
			})
		}
	}
	return runs
}
