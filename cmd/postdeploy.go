package cmd

import (
	"fmt"
	"time"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// postDeployRun identifies one notebook or data pipeline to run after a
// deploy: the deployed item, its type, and the workspace it landed in.
type postDeployRun struct {
	Name          string
	Type          string // "Notebook" or "DataPipeline" — picks the job API
	ItemID        string
	WorkspaceID   string
	WorkspaceName string
}

// postDeployRunnableTypes are the item types the post-deploy runner can
// submit as jobs — notebooks via RunNotebook, pipelines via RunPipeline.
var postDeployRunnableTypes = map[string]bool{"Notebook": true, "DataPipeline": true}

// postDeployCandidates intersects the customer's registered post-deploy
// items with what THIS deploy actually published: successful (Err == nil)
// create/update results of a runnable type whose name is registered and whose
// deployed item ID is known. Order follows the registered list (config order
// = run order); a name deployed to several workspaces yields one run per
// workspace, in results order. Returns nil when there is nothing to offer.
func postDeployCandidates(registered []string, results []deploy.Result, wsNames map[string]string) []postDeployRun {
	if len(registered) == 0 || len(results) == 0 {
		return nil
	}
	byName := map[string][]deploy.Result{}
	for _, r := range results {
		if r.Err != nil || r.Action == deploy.ActionDelete || !postDeployRunnableTypes[r.Type] || r.ID == "" {
			continue
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	var runs []postDeployRun
	seen := map[string]bool{}
	for _, name := range registered {
		for _, r := range byName[name] {
			// At most ONE run per registered name per workspace — a repo holding
			// both a Notebook and a DataPipeline under the same name must not
			// double-run (and double-load) on select-all. Results follow publish
			// order, so the Notebook wins the tie, matching pre-pipeline behavior.
			key := name + "\x00" + r.WorkspaceID
			if seen[key] {
				continue
			}
			seen[key] = true
			runs = append(runs, postDeployRun{
				Name:          name,
				Type:          r.Type,
				ItemID:        r.ID,
				WorkspaceID:   r.WorkspaceID,
				WorkspaceName: wsNames[r.WorkspaceID],
			})
		}
	}
	return runs
}

// jobRunner is the narrow slice of APIClient the post-deploy runner needs —
// kept small so tests can fake it without stubbing the full client.
type jobRunner interface {
	RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error)
	RunPipeline(token, workspaceID, itemID string) (string, error)
	GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error)
}

// postDeployOutcome records one post-deploy notebook run. Status holds the
// terminal Fabric job status ("Completed", "Failed", ...) or the synthetic
// postDeployStatusSkipped for runs never started because an earlier one failed
// — SkippedAfter then names that failed run, so the skip explains itself.
type postDeployOutcome struct {
	Run          postDeployRun
	Status       string
	Err          error
	Duration     time.Duration
	SkippedAfter string
}

// postDeployStatusSkipped marks runs that were never submitted because an
// earlier run in the sequence failed.
const postDeployStatusSkipped = "Skipped"

// runPostDeployRuns executes runs sequentially — each notebook job is
// submitted only after the previous one reached a terminal state — and stops
// at the first failure; the remainder are marked Skipped. started/finished
// are optional UI hooks (nil = silent); the runner itself never prints.
func runPostDeployRuns(client jobRunner, token string, runs []postDeployRun, started func(i, n int, r postDeployRun), finished func(o postDeployOutcome)) []postDeployOutcome {
	outcomes := make([]postDeployOutcome, 0, len(runs))
	failedName := ""
	for i, r := range runs {
		if failedName != "" {
			o := postDeployOutcome{Run: r, Status: postDeployStatusSkipped, SkippedAfter: failedName}
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
		var instanceURL string
		var err error
		if r.Type == "DataPipeline" {
			instanceURL, err = client.RunPipeline(token, r.WorkspaceID, r.ItemID)
		} else {
			instanceURL, err = client.RunNotebook(token, r.WorkspaceID, r.ItemID, nil, nil)
		}
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
			failedName = r.Name
		}
		if finished != nil {
			finished(o)
		}
		outcomes = append(outcomes, o)
	}
	return outcomes
}

// postDeployDimStyle renders skipped-run lines muted, matching the hints.
var postDeployDimStyle = lipgloss.NewStyle().Foreground(ui.DimColor)

// distinctWorkspaces counts the distinct target workspaces the runs span —
// the single fact both the picker labels and the picker title derive from.
func distinctWorkspaces(runs []postDeployRun) int {
	seen := map[string]bool{}
	for _, r := range runs {
		seen[r.WorkspaceID] = true
	}
	return len(seen)
}

// buildPostDeployPickItems renders candidates as pre-checked picker rows.
// A workspace suffix is added only when the runs span >1 workspace.
func buildPostDeployPickItems(runs []postDeployRun) []ui.CheckItem {
	if len(runs) == 0 {
		return nil
	}
	multiWS := distinctWorkspaces(runs) > 1
	items := make([]ui.CheckItem, len(runs))
	for i, r := range runs {
		label := r.Name
		if r.Type == "DataPipeline" {
			label += "  [pipeline]"
		}
		if multiWS {
			label += "  → " + r.WorkspaceName
		}
		items[i] = ui.CheckItem{Label: label, Checked: true}
	}
	return items
}

// postDeployPickerTitle names the picker after the workspace(s) the runs
// target: the single workspace name when all runs share one, or the
// distinct workspace count when they span several — naming only runs[0]'s
// workspace in that case would misrepresent the picker's contents.
func postDeployPickerTitle(runs []postDeployRun) string {
	if len(runs) == 0 {
		return "Post-deploy runs"
	}
	if n := distinctWorkspaces(runs); n > 1 {
		return fmt.Sprintf("Post-deploy runs → %d workspaces", n)
	}
	return "Post-deploy runs → " + runs[0].WorkspaceName
}

// offerPostDeployRuns is the deploy-flow tail for post-deploy runs: intersect
// the customer's registered notebooks with this run's successful deploys,
// offer the (pre-checked) intersection, and run the picked ones sequentially.
// Everything here is best-effort — a declined picker, esc, or run failures
// never fail the deploy itself. Returns the outcomes for the history report
// (nil when nothing ran).
func offerPostDeployRuns(client APIClient, token string, customer config.Customer, groups []deployGroup, results []deploy.Result) []postDeployOutcome {
	wsNames := make(map[string]string, len(groups))
	for _, g := range groups {
		wsNames[g.Target.ID] = g.Target.DisplayName
	}
	runs := postDeployCandidates(customer.PostDeployRuns, results, wsNames)
	if len(runs) == 0 {
		return nil
	}

	// skip bails out of the best-effort post-deploy phase without failing the
	// deploy (which already succeeded) — used for esc/quit, empty selection,
	// and a declined confirm.
	skip := func() []postDeployOutcome {
		fmt.Println(infoStyle.Render("Post-deploy runs skipped."))
		return nil
	}

	checked, err := ui.MultiSelectRich(postDeployPickerTitle(runs), buildPostDeployPickItems(runs))
	if err != nil {
		return skip() // esc / quit
	}
	if len(checked) == 0 {
		return skip()
	}
	picked := make([]postDeployRun, len(checked))
	for i, k := range checked {
		picked[i] = runs[k]
	}
	if ok, cerr := ui.Confirm(fmt.Sprintf("Run %d job(s)?", len(picked))); cerr != nil || !ok {
		return skip()
	}

	var sp *ui.Spinner
	outcomes := runPostDeployRuns(client, token, picked,
		func(i, n int, r postDeployRun) {
			sp = ui.NewSpinner(fmt.Sprintf("Running %d/%d: %s", i+1, n, r.Name))
			sp.Start()
		},
		func(o postDeployOutcome) {
			if sp != nil {
				sp.Stop()
				sp = nil
			}
			switch {
			case o.Status == postDeployStatusSkipped:
				fmt.Println(postDeployDimStyle.Render(fmt.Sprintf("  ⊘ %s — skipped (%s failed)", o.Run.Name, o.SkippedAfter)))
			case o.Err != nil:
				fmt.Println(errorStyle.Render(fmt.Sprintf("  ✗ %s — %v", o.Run.Name, o.Err)))
			default:
				fmt.Println(infoStyle.Render("  ✓ ") + fmt.Sprintf("%s ", o.Run.Name) + postDeployDimStyle.Render(fmt.Sprintf("— Completed in %s", o.Duration)))
			}
		})

	var completed, failed int
	for _, o := range outcomes {
		switch {
		case o.Status == postDeployStatusSkipped:
		case o.Err != nil:
			failed++
		default:
			completed++
		}
	}
	if failed > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf("Post-deploy runs: %d completed, %d failed, %d skipped", completed, failed, len(outcomes)-completed-failed)))
	} else {
		fmt.Println(successStyle.Render(fmt.Sprintf("Post-deploy runs: %d completed", completed)))
	}
	return outcomes
}
