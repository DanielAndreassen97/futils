package cmd

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestPostDeployCandidates(t *testing.T) {
	wsNames := map[string]string{"ws-1": "DP - TEST - Config", "ws-2": "DP - TEST - Data"}
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
		{Name: "NB_A", ItemID: "id-a", WorkspaceID: "ws-1", WorkspaceName: "DP - TEST - Config"},
		{Name: "NB_B", ItemID: "id-b", WorkspaceID: "ws-1", WorkspaceName: "DP - TEST - Config"},
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
		{Name: "NB_A", ItemID: "id-1", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_A", ItemID: "id-2", WorkspaceID: "ws-2", WorkspaceName: "WS Two"},
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

// fakeRunner scripts RunNotebook/GetJobInstance per item ID.
type fakeRunner struct {
	submitErr  map[string]error  // itemID -> error from RunNotebook
	status     map[string]string // itemID -> terminal job status
	failReason map[string]any    // itemID -> FailureReason on the terminal status
	pollErr    map[string]error  // itemID -> error from GetJobInstance
	submitted  []string          // itemIDs actually submitted, in order
}

func (f *fakeRunner) RunNotebook(token, workspaceID, itemID string, _ []fabric.JobInput, _ *fabric.DefaultLakehouse) (string, error) {
	f.submitted = append(f.submitted, itemID)
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
	single := buildPostDeployPickItems([]postDeployRun{
		{Name: "NB_A", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
		{Name: "NB_B", WorkspaceID: "ws-1", WorkspaceName: "WS One"},
	})
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
	})
	if !strings.Contains(multi[0].Label, "WS One") || !strings.Contains(multi[1].Label, "WS Two") {
		t.Fatalf("multi-workspace labels must carry workspace suffixes: %q / %q", multi[0].Label, multi[1].Label)
	}
}
