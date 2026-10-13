package cmd

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestPostDeployCandidates(t *testing.T) {
	wsNames := map[string]string{"ws-1": "DW - TEST - Config", "ws-2": "DW - TEST - Data"}
	results := []deploy.Result{
		{Name: "NB_B", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-b", WorkspaceID: "ws-1"},
		{Name: "NB_A", Type: "Notebook", Action: deploy.ActionCreate, ID: "id-a", WorkspaceID: "ws-1"},
		{Name: "NB_FAILED", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-f", WorkspaceID: "ws-1", Err: errors.New("boom")},
		{Name: "NB_DELETED", Type: "Notebook", Action: deploy.ActionDelete, ID: "id-d", WorkspaceID: "ws-1"},
		{Name: "SM_X", Type: "SemanticModel", Action: deploy.ActionUpdate, ID: "id-s", WorkspaceID: "ws-1"},
		{Name: "NB_UNREGISTERED", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-u", WorkspaceID: "ws-1"},
	}
	// Registered order (NB_A before NB_B) must win over results order.
	registered := []string{"NB_A", "NB_B", "NB_FAILED", "NB_DELETED", "NB_NOT_DEPLOYED"}

	got := postDeployCandidates(registered, results, wsNames)
	want := []postDeployRun{
		{Name: "NB_A", Type: "Notebook", ItemID: "id-a", WorkspaceID: "ws-1", WorkspaceName: "DW - TEST - Config"},
		{Name: "NB_B", Type: "Notebook", ItemID: "id-b", WorkspaceID: "ws-1", WorkspaceName: "DW - TEST - Config"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
}

func TestPostDeployCandidatesMultiWorkspace(t *testing.T) {
	wsNames := map[string]string{"ws-1": "WS One", "ws-2": "WS Two"}
	results := []deploy.Result{
		{Name: "NB_A", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-1", WorkspaceID: "ws-1"},
		{Name: "NB_A", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-2", WorkspaceID: "ws-2"},
		{Name: "NB_A", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-1", WorkspaceID: "ws-1"}, // duplicate
	}
	got := postDeployCandidates([]string{"NB_A"}, results, wsNames)
	want := []postDeployRun{
		{Name: "NB_A", Type: "Notebook", ItemID: "id-1", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_A", Type: "Notebook", ItemID: "id-2", WorkspaceID: "ws-2", WorkspaceName: "WS Two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
}

func TestPostDeployCandidatesEmpty(t *testing.T) {
	if got := postDeployCandidates(nil, []deploy.Result{{Name: "NB_A", Type: "Notebook", ID: "x", WorkspaceID: "w"}}, nil); got != nil {
		t.Fatalf("expected nil for empty registry, got %+v", got)
	}
	if got := postDeployCandidates([]string{"NB_A"}, nil, nil); got != nil {
		t.Fatalf("expected nil for no results, got %+v", got)
	}
}

// fakeRunner scripts RunNotebook/RunPipeline/GetJobInstance per item ID.
type fakeRunner struct {
	submitErr  map[string]error  // itemID -> error from RunNotebook/RunPipeline
	status     map[string]string // itemID -> terminal job status
	failReason map[string]any    // itemID -> FailureReason on the terminal status
	pollErr    map[string]error  // itemID -> error from GetJobInstance
	submitted  []string          // itemIDs actually submitted, in order
	pipelines  []string          // itemIDs submitted via RunPipeline specifically
	hcTags     map[string]string // itemID -> session tag, for RunNotebookHC submissions

	// barrier, when set, makes each RunNotebookHC wait until that many HC
	// submissions have arrived; a runner that submits one at a time never gets
	// there and notConcurrent is set instead of deadlocking.
	barrier       *sync.WaitGroup
	notConcurrent bool

	mu sync.Mutex
}

func (f *fakeRunner) RunNotebookHC(token, workspaceID, itemID, sessionTag string) (string, error) {
	f.mu.Lock()
	if f.hcTags == nil {
		f.hcTags = map[string]string{}
	}
	f.hcTags[itemID] = sessionTag
	f.mu.Unlock()
	if f.barrier != nil {
		f.barrier.Done()
		done := make(chan struct{})
		go func() { f.barrier.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			f.mu.Lock()
			f.notConcurrent = true
			f.mu.Unlock()
		}
	}
	return f.RunNotebook(token, workspaceID, itemID, nil, nil)
}

func (f *fakeRunner) RunPipeline(token, workspaceID, itemID string, _ map[string]any) (string, error) {
	f.mu.Lock()
	f.pipelines = append(f.pipelines, itemID)
	f.mu.Unlock()
	return f.RunNotebook(token, workspaceID, itemID, nil, nil)
}

func (f *fakeRunner) RunNotebook(token, workspaceID, itemID string, _ []fabric.JobInput, _ *fabric.DefaultLakehouse) (string, error) {
	f.mu.Lock()
	f.submitted = append(f.submitted, itemID)
	f.mu.Unlock()
	if err := f.submitErr[itemID]; err != nil {
		return "", err
	}
	return "instance-" + itemID, nil
}

func (f *fakeRunner) GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error) {
	itemID := strings.TrimPrefix(instanceURL, "instance-")
	if err := f.pollErr[itemID]; err != nil {
		return fabric.JobInstanceStatus{}, err
	}
	st := f.status[itemID]
	if st == "" {
		st = fabric.JobStatusCompleted
	}
	return fabric.JobInstanceStatus{Status: st, FailureReason: f.failReason[itemID]}, nil
}

func TestRunPostDeployRunsAllComplete(t *testing.T) {
	f := &fakeRunner{}
	runs := []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
		{Name: "NB_B", ItemID: "b", WorkspaceID: "ws-1"},
	}
	out := runPostDeployRuns(f, "tok", runs, nil, nil)
	if len(out) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(out))
	}
	for i, o := range out {
		if o.Status != fabric.JobStatusCompleted || o.Err != nil {
			t.Fatalf("outcome %d = %+v, want Completed", i, o)
		}
	}
	if !reflect.DeepEqual(f.submitted, []string{"a", "b"}) {
		t.Fatalf("submitted = %v, want [a b] (sequential, in order)", f.submitted)
	}
}

func TestRunPostDeployRunsStopsOnFailure(t *testing.T) {
	f := &fakeRunner{status: map[string]string{"b": fabric.JobStatusFailed}}
	runs := []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
		{Name: "NB_B", ItemID: "b", WorkspaceID: "ws-1"},
		{Name: "NB_C", ItemID: "c", WorkspaceID: "ws-1"},
	}
	out := runPostDeployRuns(f, "tok", runs, nil, nil)
	if len(out) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(out))
	}
	if out[0].Status != fabric.JobStatusCompleted {
		t.Fatalf("first = %+v, want Completed", out[0])
	}
	if out[1].Status != fabric.JobStatusFailed || out[1].Err == nil {
		t.Fatalf("second = %+v, want Failed with error", out[1])
	}
	if out[2].Status != postDeployStatusSkipped {
		t.Fatalf("third = %+v, want Skipped", out[2])
	}
	if !reflect.DeepEqual(f.submitted, []string{"a", "b"}) {
		t.Fatalf("submitted = %v — NB_C must never be submitted after a failure", f.submitted)
	}
}

func TestRunPostDeployRunsFailureReason(t *testing.T) {
	f := &fakeRunner{
		status:     map[string]string{"a": fabric.JobStatusFailed},
		failReason: map[string]any{"a": "NotebookExecutionFailed: division by zero"},
	}
	out := runPostDeployRuns(f, "tok", []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
	}, nil, nil)
	if out[0].Status != fabric.JobStatusFailed || out[0].Err == nil {
		t.Fatalf("outcome = %+v, want Failed with error", out[0])
	}
	if !strings.Contains(out[0].Err.Error(), "NotebookExecutionFailed: division by zero") {
		t.Fatalf("err = %q, want it to contain the failure reason", out[0].Err)
	}
}

func TestRunPostDeployRunsPollError(t *testing.T) {
	pollErr := errors.New("connection reset")
	f := &fakeRunner{pollErr: map[string]error{"a": pollErr}}
	out := runPostDeployRuns(f, "tok", []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
		{Name: "NB_B", ItemID: "b", WorkspaceID: "ws-1"},
	}, nil, nil)
	if out[0].Status != fabric.JobStatusFailed || !errors.Is(out[0].Err, pollErr) {
		t.Fatalf("first = %+v, want Failed wrapping the poll error", out[0])
	}
	if out[1].Status != postDeployStatusSkipped {
		t.Fatalf("second = %+v, want Skipped", out[1])
	}
	if !reflect.DeepEqual(f.submitted, []string{"a"}) {
		t.Fatalf("submitted = %v — NB_B must never be submitted after a poll error", f.submitted)
	}
}

func TestRunPostDeployRunsSubmitError(t *testing.T) {
	f := &fakeRunner{submitErr: map[string]error{"a": errors.New("403")}}
	out := runPostDeployRuns(f, "tok", []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
		{Name: "NB_B", ItemID: "b", WorkspaceID: "ws-1"},
	}, nil, nil)
	if out[0].Status != fabric.JobStatusFailed || out[0].Err == nil {
		t.Fatalf("first = %+v, want Failed", out[0])
	}
	if out[1].Status != postDeployStatusSkipped {
		t.Fatalf("second = %+v, want Skipped", out[1])
	}
}

func TestBuildPostDeployPickItems(t *testing.T) {
	if items := buildPostDeployPickItems(nil, nil); items != nil {
		t.Fatalf("empty runs must yield nil items, got %v", items)
	}

	single := buildPostDeployPickItems([]postDeployRun{
		{Name: "NB_A", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_B", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
	}, nil)
	if len(single) != 2 {
		t.Fatalf("got %d items, want 2", len(single))
	}
	for i, it := range single {
		if !it.Checked {
			t.Fatalf("item %d not pre-checked", i)
		}
		if strings.Contains(it.Label, "WS One") {
			t.Fatalf("single-workspace label %q must not carry a workspace suffix", it.Label)
		}
	}

	multi := buildPostDeployPickItems([]postDeployRun{
		{Name: "NB_A", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_A", WorkspaceID: "ws-2", WorkspaceName: "WS Two"},
	}, nil)
	if !strings.Contains(multi[0].Label, "WS One") || !strings.Contains(multi[1].Label, "WS Two") {
		t.Fatalf("multi-workspace labels must carry workspace suffixes: %q / %q", multi[0].Label, multi[1].Label)
	}
}

// In parallel mode each row carries its phase, so the picker shows what will
// run together; sequential mode keeps the plain names.
func TestBuildPostDeployPickItemsShowsPhaseInParallelMode(t *testing.T) {
	items := buildPostDeployPickItems([]postDeployRun{nb("NB_Config", "ws"), nb("NB_A", "ws")}, phases(map[string]int{"NB_A": 2}))
	if items[0].Label != "[P1] NB_Config" || items[1].Label != "[P2] NB_A" {
		t.Fatalf("labels = %q, %q", items[0].Label, items[1].Label)
	}
}

func TestPostDeployPickerTitle(t *testing.T) {
	single := postDeployPickerTitle([]postDeployRun{
		{Name: "NB_A", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_B", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
	})
	if single != "Post-deploy runs → WS One" {
		t.Fatalf("single-workspace title = %q, want the workspace name", single)
	}

	multi := postDeployPickerTitle([]postDeployRun{
		{Name: "NB_A", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_A", WorkspaceID: "ws-2", WorkspaceName: "WS Two"},
	})
	if multi != "Post-deploy runs → 2 workspaces" {
		t.Fatalf("multi-workspace title = %q, want a distinct-count summary", multi)
	}
}

func TestRunPostDeployRunsHooks(t *testing.T) {
	f := &fakeRunner{status: map[string]string{"b": fabric.JobStatusFailed}}
	runs := []postDeployRun{
		{Name: "NB_A", ItemID: "a", WorkspaceID: "ws-1"},
		{Name: "NB_B", ItemID: "b", WorkspaceID: "ws-1"},
		{Name: "NB_C", ItemID: "c", WorkspaceID: "ws-1"},
	}
	var startedNames, finishedNames []string
	out := runPostDeployRuns(f, "tok", runs,
		func(i, n int, r postDeployRun) { startedNames = append(startedNames, r.Name) },
		func(o postDeployOutcome) { finishedNames = append(finishedNames, o.Run.Name) })
	if !reflect.DeepEqual(startedNames, []string{"NB_A", "NB_B"}) {
		t.Fatalf("started = %v — must not fire for skipped NB_C", startedNames)
	}
	if !reflect.DeepEqual(finishedNames, []string{"NB_A", "NB_B", "NB_C"}) {
		t.Fatalf("finished = %v — must fire once per outcome incl. skipped", finishedNames)
	}
	if len(out) != 3 || out[2].Status != postDeployStatusSkipped {
		t.Fatalf("outcomes = %+v", out)
	}
}

// Pipelines are post-deploy runnable alongside notebooks: candidates include
// deployed DataPipeline results, and the runner submits them through the
// pipeline job API, not the notebook one.
func TestPostDeployPipelines(t *testing.T) {
	results := []deploy.Result{
		{Name: "nb_ingest", Type: "Notebook", Action: deploy.ActionUpdate, ID: "nb-1", WorkspaceID: "ws-1"},
		{Name: "PL_refresh", Type: "DataPipeline", Action: deploy.ActionUpdate, ID: "pl-1", WorkspaceID: "ws-1"},
		{Name: "WH_x", Type: "Warehouse", Action: deploy.ActionCreate, ID: "wh-1", WorkspaceID: "ws-1"},
	}
	runs := postDeployCandidates([]string{"PL_refresh", "nb_ingest", "WH_x"}, results, map[string]string{"ws-1": "WS"})
	if len(runs) != 2 {
		t.Fatalf("candidates = %+v, want pipeline+notebook (never the warehouse)", runs)
	}
	if runs[0].Name != "PL_refresh" || runs[0].Type != "DataPipeline" {
		t.Errorf("registered order must win and carry the type: %+v", runs[0])
	}

	f := &fakeRunner{status: map[string]string{"pl-1": fabric.JobStatusCompleted, "nb-1": fabric.JobStatusCompleted}}
	outcomes := runPostDeployRuns(f, "tok", runs, nil, nil)
	if len(outcomes) != 2 || outcomes[0].Err != nil || outcomes[1].Err != nil {
		t.Fatalf("outcomes: %+v", outcomes)
	}
	if len(f.pipelines) != 1 || f.pipelines[0] != "pl-1" {
		t.Errorf("pipeline must be submitted via RunPipeline, got %v", f.pipelines)
	}
}

// One registered name deployed as BOTH a Notebook and a DataPipeline in the
// same workspace must yield exactly one run — double-submitting both jobs
// could double-load the freshly deployed target. Results follow publish order
// (Notebook before DataPipeline), so the notebook wins the tie.
func TestPostDeployCandidatesSameNameNotebookAndPipeline(t *testing.T) {
	results := []deploy.Result{
		{Name: "Load_Data", Type: "Notebook", ID: "nb-1", WorkspaceID: "ws-1"},
		{Name: "Load_Data", Type: "DataPipeline", ID: "pl-1", WorkspaceID: "ws-1"},
		{Name: "Load_Data", Type: "DataPipeline", ID: "pl-2", WorkspaceID: "ws-2"},
	}
	runs := postDeployCandidates([]string{"Load_Data"}, results, map[string]string{})
	if len(runs) != 2 {
		t.Fatalf("want one run per workspace, got %+v", runs)
	}
	if runs[0].Type != "Notebook" || runs[0].ItemID != "nb-1" || runs[0].WorkspaceID != "ws-1" {
		t.Errorf("ws-1 must run the notebook only, got %+v", runs[0])
	}
	if runs[1].Type != "DataPipeline" || runs[1].ItemID != "pl-2" || runs[1].WorkspaceID != "ws-2" {
		t.Errorf("ws-2 has only the pipeline, got %+v", runs[1])
	}
}

// stepShape renders a plan compactly for comparison: one entry per step,
// "tag:names" with names joined by +, tag empty for a solo run.
func stepShape(steps []postDeployStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		names := make([]string, len(s.Runs))
		for j, r := range s.Runs {
			names[j] = r.Name
		}
		out[i] = s.SessionTag + ":" + strings.Join(names, "+")
	}
	return out
}

func nb(name, ws string) postDeployRun {
	return postDeployRun{Name: name, Type: "Notebook", ItemID: "id-" + name, WorkspaceID: ws}
}

func phases(m map[string]int) func(string) int {
	return func(n string) int {
		if p := m[n]; p > 1 {
			return p
		}
		return 1
	}
}

// Sequential mode is today's behaviour: one run per step, in list order —
// except that phases, when set, still order the runs.
func TestPlanPostDeploySequential(t *testing.T) {
	runs := []postDeployRun{nb("A", "ws"), nb("B", "ws"), nb("C", "ws")}
	got := stepShape(planPostDeploySteps(runs, phases(map[string]int{"A": 2}), false, "t"))
	want := []string{":B", ":C", ":A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

// Parallel mode: phases in order; a phase's notebooks go into balanced groups
// of at most five (11 -> 4,4,3, never 5,5,1), each with its own tag; a group
// of one runs alone without a tag (high concurrency only pays off when runs
// overlap).
func TestPlanPostDeployParallelGroups(t *testing.T) {
	var runs []postDeployRun
	runs = append(runs, nb("Config", "ws"))
	for i := 1; i <= 11; i++ {
		runs = append(runs, nb(fmt.Sprintf("I%02d", i), "ws"))
	}
	ph := map[string]int{}
	for _, r := range runs[1:] {
		ph[r.Name] = 2
	}
	got := stepShape(planPostDeploySteps(runs, phases(ph), true, "futils_postdeploy_9"))
	want := []string{
		":Config",
		"futils_postdeploy_9_p2g1:I01+I02+I03+I04",
		"futils_postdeploy_9_p2g2:I05+I06+I07+I08",
		"futils_postdeploy_9_p2g3:I09+I10+I11",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%v\nwant\n%v", got, want)
	}
}

// A high-concurrency session cannot span workspaces, so a phase splits by
// workspace first. Pipelines have no session to share: they run one at a time
// after the phase's notebook groups.
func TestPlanPostDeployParallelWorkspacesAndPipelines(t *testing.T) {
	pl := postDeployRun{Name: "PL", Type: "DataPipeline", ItemID: "id-PL", WorkspaceID: "ws1"}
	runs := []postDeployRun{pl, nb("A", "ws1"), nb("X", "ws2"), nb("B", "ws1"), nb("Y", "ws2"), nb("Z", "ws3")}
	got := stepShape(planPostDeploySteps(runs, phases(nil), true, "t"))
	want := []string{"t_p1g1:A+B", "t_p1g2:X+Y", ":Z", ":PL"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	for _, tag := range []string{"t_p1g1", "t_p1g2"} {
		if !fabric.ValidSessionTag(tag) {
			t.Errorf("planned tag %q is not a valid Fabric session tag", tag)
		}
	}
}

// A tagged step submits its notebooks together with the step's session tag —
// that overlap is what lets Fabric pack them into one session. A failure
// inside the group still lets the rest of the group finish (they are already
// running), but nothing after the group is submitted.
func TestRunPostDeployStepsGroupRunsConcurrentlyAndStopsAfter(t *testing.T) {
	barrier := &sync.WaitGroup{}
	barrier.Add(3)
	f := &fakeRunner{barrier: barrier, status: map[string]string{"id-B": fabric.JobStatusFailed}}
	steps := []postDeployStep{
		{Phase: 1, Runs: []postDeployRun{nb("A", "ws"), nb("B", "ws"), nb("C", "ws")}, SessionTag: "t_p1g1"},
		{Phase: 2, Runs: []postDeployRun{nb("D", "ws")}},
	}
	var finished []string
	out := runPostDeploySteps(f, "tok", steps, nil, func(o postDeployOutcome) { finished = append(finished, o.Run.Name) })

	if f.notConcurrent {
		t.Fatal("group members were not submitted concurrently")
	}
	for _, id := range []string{"id-A", "id-B", "id-C"} {
		if f.hcTags[id] != "t_p1g1" {
			t.Errorf("%s tag = %q, want t_p1g1", id, f.hcTags[id])
		}
	}
	if len(out) != 4 {
		t.Fatalf("outcomes = %+v", out)
	}
	// Plan order, not completion order.
	for i, want := range []string{"A", "B", "C", "D"} {
		if out[i].Run.Name != want {
			t.Errorf("outcome %d = %s, want %s", i, out[i].Run.Name, want)
		}
	}
	if out[0].Err != nil || out[2].Err != nil {
		t.Errorf("A and C must complete despite B failing: %+v / %+v", out[0], out[2])
	}
	if out[1].Err == nil {
		t.Errorf("B must fail: %+v", out[1])
	}
	if out[3].Status != postDeployStatusSkipped || out[3].SkippedAfter != "B" {
		t.Errorf("D = %+v, want Skipped after B", out[3])
	}
	if slices.Contains(f.submitted, "id-D") {
		t.Error("D must never be submitted after a failed group")
	}
	if len(finished) != 4 {
		t.Errorf("finished fired %d times, want 4", len(finished))
	}
}

// An untagged step uses the regular notebook endpoint, never the HC one —
// a lone notebook gains nothing from a high-concurrency session.
func TestRunPostDeployStepsUntaggedUsesRegularEndpoint(t *testing.T) {
	f := &fakeRunner{}
	runPostDeploySteps(f, "tok", []postDeployStep{{Phase: 1, Runs: []postDeployRun{nb("A", "ws")}}}, nil, nil)
	if len(f.hcTags) != 0 {
		t.Errorf("solo run went through RunNotebookHC: %v", f.hcTags)
	}
	if !reflect.DeepEqual(f.submitted, []string{"id-A"}) {
		t.Errorf("submitted = %v", f.submitted)
	}
}
