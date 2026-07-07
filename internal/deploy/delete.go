package deploy

import (
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// DeleteTarget is a target-only item (an orphan) the user chose to delete.
type DeleteTarget struct {
	ID   string
	Name string
	Type string
}

// DeleteItems removes each target from the workspace, returning a per-target
// Result (Action=ActionDelete; Err set on failure, like Execute). It is kept
// separate from Execute so the destructive path is isolated and auditable.
// Callers MUST confirm before invoking — deletions are irreversible.
func DeleteItems(client FabricClient, token string, target fabric.Workspace, targets []DeleteTarget) []Result {
	results := make([]Result, 0, len(targets))
	for _, t := range targets {
		res := Result{Name: t.Name, Type: t.Type, Action: ActionDelete, WorkspaceID: target.ID}
		if err := client.DeleteItem(token, target.ID, t.ID); err != nil {
			res.Err = fmt.Errorf("delete %s: %w", t.Name, err)
		}
		results = append(results, res)
	}
	return results
}
