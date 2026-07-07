package cmd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
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
