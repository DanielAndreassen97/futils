package cmd

import (
	"fmt"
	"slices"
	"strings"
	"sync"
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

// postDeployStep is one unit the runner submits and waits for before moving
// on: a single run, or a group of notebooks submitted together that share a
// high-concurrency Spark session through SessionTag.
type postDeployStep struct {
	Phase      int
	Runs       []postDeployRun
	SessionTag string // empty = one run on the regular endpoint
}

// maxHCGroupSize is how many notebooks Fabric packs into one high-concurrency
// session; a sixth with the same tag would start a second session anyway.
const maxHCGroupSize = 5

// planPostDeploySteps cuts runs into steps. Sequential: one run per step in
// list order, exactly as before phases existed; run order is a parallel-mode
// setting and is neither shown nor applied here. Parallel: runs are ordered by
// phase (stable, so list order breaks ties), then per phase and per workspace (a session cannot span
// workspaces), notebooks are split into balanced groups of at most
// maxHCGroupSize — balanced so 11 becomes 4+4+3 rather than leaving a lone
// fifth-session notebook. A group of two or more shares one tag; a group of
// one runs untagged, since high concurrency only pays off when runs overlap.
// Pipelines cannot share a session and run one at a time after the phase's
// notebooks. tagPrefix must itself be a valid session tag.
func planPostDeploySteps(runs []postDeployRun, phaseOf func(name string) int, parallel bool, tagPrefix string) []postDeployStep {
	var steps []postDeployStep
	if !parallel {
		for _, r := range runs {
			steps = append(steps, postDeployStep{Phase: 1, Runs: []postDeployRun{r}})
		}
		return steps
	}

	sorted := slices.Clone(runs)
	slices.SortStableFunc(sorted, func(a, b postDeployRun) int { return phaseOf(a.Name) - phaseOf(b.Name) })

	for start := 0; start < len(sorted); {
		phase := phaseOf(sorted[start].Name)
		end := start
		for end < len(sorted) && phaseOf(sorted[end].Name) == phase {
			end++
		}
		var wsOrder []string
		notebooks := map[string][]postDeployRun{}
		var pipelines []postDeployRun
		for _, r := range sorted[start:end] {
			if r.Type == "DataPipeline" {
				pipelines = append(pipelines, r)
				continue
			}
			if _, seen := notebooks[r.WorkspaceID]; !seen {
				wsOrder = append(wsOrder, r.WorkspaceID)
			}
			notebooks[r.WorkspaceID] = append(notebooks[r.WorkspaceID], r)
		}
		group := 0
		for _, ws := range wsOrder {
			for _, g := range balancedGroups(notebooks[ws], maxHCGroupSize) {
				step := postDeployStep{Phase: phase, Runs: g}
				if len(g) > 1 {
					group++
					step.SessionTag = fmt.Sprintf("%s_p%dg%d", tagPrefix, phase, group)
				}
				steps = append(steps, step)
			}
		}
		for _, p := range pipelines {
			steps = append(steps, postDeployStep{Phase: phase, Runs: []postDeployRun{p}})
		}
		start = end
	}
	return steps
}

// balancedGroups splits runs into the fewest groups of at most size, as
// evenly as possible, keeping order: 11 at size 5 -> 4, 4, 3.
func balancedGroups(runs []postDeployRun, size int) [][]postDeployRun {
	if len(runs) == 0 {
		return nil
	}
	n := (len(runs) + size - 1) / size
	base, extra := len(runs)/n, len(runs)%n
	groups := make([][]postDeployRun, 0, n)
	for i, at := 0, 0; i < n; i++ {
		k := base
		if i < extra {
			k++
		}
		groups = append(groups, runs[at:at+k])
		at += k
	}
	return groups
}

// jobRunner is the narrow slice of APIClient the post-deploy runner needs —
// kept small so tests can fake it without stubbing the full client.
type jobRunner interface {
	RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error)
	RunNotebookHC(token, workspaceID, itemID, sessionTag string) (string, error)
	RunPipeline(token, workspaceID, itemID string, params map[string]any) (string, error)
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

// runPostDeploySteps executes steps one after another. The runs inside a step
// are submitted together and polled concurrently, so a tagged group lands in
// one shared Spark session; the step ends when every run in it is terminal.
// A started step always finishes, but once any run has failed every later
// step is marked Skipped without being submitted. Outcomes come back in plan
// order; finished fires as each run completes (serialised, so hooks may
// print), and once per skipped run.
func runPostDeploySteps(client jobRunner, token string, steps []postDeployStep, started func(i, n int, s postDeployStep), finished func(o postDeployOutcome)) []postDeployOutcome {
	var outcomes []postDeployOutcome
	failedName := ""
	for i, step := range steps {
		if failedName != "" {
			for _, r := range step.Runs {
				o := postDeployOutcome{Run: r, Status: postDeployStatusSkipped, SkippedAfter: failedName}
				if finished != nil {
					finished(o)
				}
				outcomes = append(outcomes, o)
			}
			continue
		}
		if started != nil {
			started(i, len(steps), step)
		}
		results := make([]postDeployOutcome, len(step.Runs))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for k, r := range step.Runs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				o := runPostDeployJob(client, token, r, step.SessionTag)
				mu.Lock()
				defer mu.Unlock()
				results[k] = o
				if finished != nil {
					finished(o)
				}
			}()
		}
		wg.Wait()
		for _, o := range results {
			if o.Err != nil && failedName == "" {
				failedName = o.Run.Name
			}
		}
		outcomes = append(outcomes, results...)
	}
	return outcomes
}

// runPostDeployJob submits one run through the API that fits it — pipeline
// job, high-concurrency notebook job when tagged, plain notebook job
// otherwise — and polls it to a terminal state.
func runPostDeployJob(client jobRunner, token string, r postDeployRun, sessionTag string) postDeployOutcome {
	begin := time.Now()
	o := postDeployOutcome{Run: r}
	var instanceURL string
	var err error
	switch {
	case r.Type == "DataPipeline":
		instanceURL, err = client.RunPipeline(token, r.WorkspaceID, r.ItemID, nil)
	case sessionTag != "":
		instanceURL, err = client.RunNotebookHC(token, r.WorkspaceID, r.ItemID, sessionTag)
	default:
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
	return o
}

// printPostDeployPlan shows a parallel plan before the confirm: one line per
// step, so it is clear which notebooks will share a Spark session.
func printPostDeployPlan(steps []postDeployStep) {
	fmt.Println()
	fmt.Println(infoStyle.Render("Post-deploy plan (parallel):"))
	for i, s := range steps {
		names := make([]string, len(s.Runs))
		for k, r := range s.Runs {
			names[k] = r.Name
		}
		kind := "alone"
		if s.SessionTag != "" {
			kind = fmt.Sprintf("together, shared Spark session (%d)", len(s.Runs))
		} else if s.Runs[0].Type == "DataPipeline" {
			kind = "alone, pipeline"
		}
		fmt.Printf("  %d. %s  %s\n", i+1, postDeployDimStyle.Render(fmt.Sprintf("run order %d · %s", s.Phase, kind)), strings.Join(names, ", "))
	}
	fmt.Println()
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
// A workspace suffix is added only when the runs span >1 workspace. phaseOf,
// when non-nil (parallel mode), prefixes each row with its phase so the
// picker shows what runs together.
func buildPostDeployPickItems(runs []postDeployRun, phaseOf func(string) int) []ui.CheckItem {
	if len(runs) == 0 {
		return nil
	}
	multiWS := distinctWorkspaces(runs) > 1
	items := make([]ui.CheckItem, len(runs))
	for i, r := range runs {
		label := r.Name
		if phaseOf != nil {
			label = fmt.Sprintf("[%d] %s", phaseOf(r.Name), label)
		}
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
// offer the (pre-checked) intersection, and run the picked ones — one after
// another, or in phases of shared-session groups when the customer has
// parallel post-deploy on.
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

	var pickPhase func(string) int
	if customer.ParallelPostDeploy {
		pickPhase = customer.PostDeployPhase
	}
	checked, err := ui.MultiSelectRich(postDeployPickerTitle(runs), buildPostDeployPickItems(runs, pickPhase))
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
	tagPrefix := fmt.Sprintf("futils_postdeploy_%d", time.Now().Unix())
	steps := planPostDeploySteps(picked, customer.PostDeployPhase, customer.ParallelPostDeploy, tagPrefix)

	question := fmt.Sprintf("Run %d job(s) one after another?", len(picked))
	if customer.ParallelPostDeploy {
		printPostDeployPlan(steps)
		question = fmt.Sprintf("Run %d job(s) in %d step(s)?", len(picked), len(steps))
	}
	if ok, cerr := ui.Confirm(question); cerr != nil || !ok {
		return skip()
	}

	// remaining counts the current step's runs still in flight, so the spinner
	// keeps going between the completions of a parallel group.
	var sp *ui.Spinner
	var stepLabel string
	remaining := 0
	outcomes := runPostDeploySteps(client, token, steps,
		func(i, n int, s postDeployStep) {
			remaining = len(s.Runs)
			if len(s.Runs) == 1 {
				stepLabel = fmt.Sprintf("Running %d/%d: %s", i+1, n, s.Runs[0].Name)
			} else {
				stepLabel = fmt.Sprintf("Running %d/%d: run order %d, %d notebooks in one shared Spark session", i+1, n, s.Phase, len(s.Runs))
			}
			sp = ui.NewSpinner(stepLabel)
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
			if remaining > 0 {
				remaining--
				if remaining > 0 {
					sp = ui.NewSpinner(fmt.Sprintf("%s (%d still running)", stepLabel, remaining))
					sp.Start()
				}
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
