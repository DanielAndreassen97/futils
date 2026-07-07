package cmd

import (
	"fmt"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
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

// notebookRunner is the narrow slice of APIClient the post-deploy runner
// needs — kept small so tests can fake it without stubbing the full client.
type notebookRunner interface {
	RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error)
	GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error)
}

// postDeployOutcome records one post-deploy notebook run. Status holds the
// terminal Fabric job status ("Completed", "Failed", ...) or the synthetic
// postDeployStatusSkipped for runs never started because an earlier one failed.
type postDeployOutcome struct {
	Run      postDeployRun
	Status   string
	Err      error
	Duration time.Duration
}

// postDeployStatusSkipped marks runs that were never submitted because an
// earlier run in the sequence failed.
const postDeployStatusSkipped = "Skipped"

// runPostDeployRuns executes runs sequentially — each notebook job is
// submitted only after the previous one reached a terminal state — and stops
// at the first failure; the remainder are marked Skipped. started/finished
// are optional UI hooks (nil = silent); the runner itself never prints.
func runPostDeployRuns(client notebookRunner, token string, runs []postDeployRun, started func(i, n int, r postDeployRun), finished func(o postDeployOutcome)) []postDeployOutcome {
	outcomes := make([]postDeployOutcome, 0, len(runs))
	failed := false
	for i, r := range runs {
		if failed {
			o := postDeployOutcome{Run: r, Status: postDeployStatusSkipped}
			if finished != nil {
				finished(o)
			}
			outcomes = append(outcomes, o)
			continue
		}
		if started != nil {
			started(i, len(runs), r)
		}
		begin := time.Now()
		o := postDeployOutcome{Run: r}
		instanceURL, err := client.RunNotebook(token, r.WorkspaceID, r.ItemID, nil, nil)
		if err != nil {
			o.Status, o.Err = fabric.JobStatusFailed, fmt.Errorf("submit job: %w", err)
		} else {
			status, perr := pollJob(client, token, instanceURL)
			o.Status = status.Status
			switch {
			case perr != nil:
				o.Status, o.Err = fabric.JobStatusFailed, perr
			case status.Status != fabric.JobStatusCompleted:
				if status.FailureReason != nil {
					o.Err = fmt.Errorf("job %s: %v", status.Status, status.FailureReason)
				} else {
					o.Err = fmt.Errorf("job %s", status.Status)
				}
			}
		}
		o.Duration = time.Since(begin).Round(time.Second)
		if o.Err != nil {
			failed = true
		}
		if finished != nil {
			finished(o)
		}
		outcomes = append(outcomes, o)
	}
	return outcomes
}
