package fabric

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// stubTransport swaps the package HTTP client for the duration of a test and
// returns the transport so the test can assert on the requests that were made.
func stubTransport(t *testing.T, responses ...seqResponse) *seqTransport {
	t.Helper()
	transport := &seqTransport{responses: responses}
	orig := httpClient
	t.Cleanup(func() { httpClient = orig })
	httpClient = &http.Client{Transport: transport}
	return transport
}

// decodeBody unmarshals a recorded request body into a generic map so tests can
// assert on which keys were sent — omitted-vs-empty is the distinction that
// matters for PATCH.
func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("request body is not JSON (%q): %v", string(raw), err)
	}
	return m
}

const (
	testWSID  = "11111111-1111-1111-1111-111111111111"
	testCapID = "22222222-2222-2222-2222-222222222222"
)

func TestCreateWorkspaceSendsAllProvidedFields(t *testing.T) {
	transport := stubTransport(t, seqResponse{status: 201, body: `{
	  "id": "` + testWSID + `",
	  "displayName": "DW - Finance",
	  "description": "Finance data",
	  "type": "Workspace",
	  "capacityId": "` + testCapID + `",
	  "capacityRegion": "Norway East"
	}`})

	ws, err := CreateWorkspace("tok", "DW - Finance", "Finance data", testCapID)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if ws.ID != testWSID || ws.DisplayName != "DW - Finance" {
		t.Errorf("decoded workspace = %+v", ws)
	}
	if ws.Description != "Finance data" || ws.Type != "Workspace" || ws.CapacityID != testCapID {
		t.Errorf("new Workspace fields not decoded: %+v", ws)
	}
	if len(transport.urls) != 1 || transport.urls[0] != baseURL+"/v1/workspaces" {
		t.Fatalf("urls = %v", transport.urls)
	}
	body := decodeBody(t, transport.bodies[0])
	if body["displayName"] != "DW - Finance" || body["description"] != "Finance data" || body["capacityId"] != testCapID {
		t.Errorf("request body = %v", body)
	}
}

func TestCreateWorkspaceOmitsEmptyOptionalFields(t *testing.T) {
	// A workspace created without a capacity must not send capacityId at all —
	// Fabric rejects an empty string where it expects a uuid.
	transport := stubTransport(t, seqResponse{status: 201, body: `{"id":"` + testWSID + `","displayName":"Sandbox"}`})

	if _, err := CreateWorkspace("tok", "Sandbox", "", ""); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	body := decodeBody(t, transport.bodies[0])
	if _, ok := body["capacityId"]; ok {
		t.Error("capacityId must be omitted when no capacity is chosen")
	}
	if _, ok := body["description"]; ok {
		t.Error("description must be omitted when empty")
	}
	if body["displayName"] != "Sandbox" {
		t.Errorf("displayName = %v", body["displayName"])
	}
}

func TestCreateWorkspaceReportsAPIError(t *testing.T) {
	stubTransport(t, seqResponse{status: 400, body: `{"errorCode":"WorkspaceNameAlreadyExists"}`})

	_, err := CreateWorkspace("tok", "Taken", "", "")
	if err == nil {
		t.Fatal("expected an error on 400")
	}
	if !strings.Contains(err.Error(), "WorkspaceNameAlreadyExists") {
		t.Errorf("error must surface Fabric's own message, got %v", err)
	}
}

func TestCreateWorkspaceRejectsInvalidCapacityID(t *testing.T) {
	stubTransport(t, seqResponse{status: 201, body: `{}`})

	if _, err := CreateWorkspace("tok", "Sandbox", "", "not-a-uuid"); err == nil {
		t.Fatal("expected a validation error for a non-UUID capacity id")
	}
}

func TestRenameWorkspacePatchesDisplayNameOnly(t *testing.T) {
	// Sending description alongside the rename would overwrite whatever the
	// workspace has today with a possibly stale local copy.
	transport := stubTransport(t, seqResponse{status: 200, body: `{"id":"` + testWSID + `","displayName":"DW - Finance PROD"}`})

	ws, err := RenameWorkspace("tok", testWSID, "DW - Finance PROD")
	if err != nil {
		t.Fatalf("RenameWorkspace: %v", err)
	}
	if ws.DisplayName != "DW - Finance PROD" {
		t.Errorf("displayName = %q", ws.DisplayName)
	}
	if transport.urls[0] != baseURL+"/v1/workspaces/"+testWSID {
		t.Errorf("url = %q", transport.urls[0])
	}
	body := decodeBody(t, transport.bodies[0])
	if body["displayName"] != "DW - Finance PROD" {
		t.Errorf("displayName = %v", body["displayName"])
	}
	if _, ok := body["description"]; ok {
		t.Error("rename must not send description")
	}
}

func TestSetWorkspaceDescriptionSendsEmptyStringToClear(t *testing.T) {
	// An empty description is a real value here — it clears the field — so it
	// must be sent, not omitted.
	transport := stubTransport(t, seqResponse{status: 200, body: `{"id":"` + testWSID + `","displayName":"Sandbox"}`})

	if _, err := SetWorkspaceDescription("tok", testWSID, ""); err != nil {
		t.Fatalf("SetWorkspaceDescription: %v", err)
	}
	body := decodeBody(t, transport.bodies[0])
	desc, ok := body["description"]
	if !ok {
		t.Fatal("description must always be sent, including when empty")
	}
	if desc != "" {
		t.Errorf("description = %v, want empty string", desc)
	}
	if _, ok := body["displayName"]; ok {
		t.Error("setting the description must not send displayName")
	}
}

func TestPatchWorkspaceForbiddenMentionsAdminRole(t *testing.T) {
	// Fabric answers 403 with a generic body; the flow pre-checks the role, but
	// a stale role cache must still produce an actionable message.
	stubTransport(t, seqResponse{status: 403, body: `{"errorCode":"InsufficientPrivileges"}`})

	_, err := RenameWorkspace("tok", testWSID, "New name")
	if err == nil {
		t.Fatal("expected an error on 403")
	}
	if !strings.Contains(err.Error(), "Admin") {
		t.Errorf("403 must mention the Admin workspace role, got %v", err)
	}
}

func TestDeleteWorkspaceUsesDeleteVerb(t *testing.T) {
	transport := stubTransport(t, seqResponse{status: 200, body: ``})

	if err := DeleteWorkspace("tok", testWSID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if transport.methods[0] != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", transport.methods[0])
	}
	if transport.urls[0] != baseURL+"/v1/workspaces/"+testWSID {
		t.Errorf("url = %q", transport.urls[0])
	}
}

func TestDeleteWorkspaceForbiddenMentionsAdminRole(t *testing.T) {
	stubTransport(t, seqResponse{status: 403, body: `{"errorCode":"InsufficientPrivileges"}`})

	err := DeleteWorkspace("tok", testWSID)
	if err == nil {
		t.Fatal("expected an error on 403")
	}
	if !strings.Contains(err.Error(), "Admin") {
		t.Errorf("403 must mention the Admin workspace role, got %v", err)
	}
}

func TestDeleteWorkspaceRejectsInvalidID(t *testing.T) {
	if err := DeleteWorkspace("tok", "not-a-uuid"); err == nil {
		t.Fatal("expected a validation error for a non-UUID workspace id")
	}
}

func TestListWorkspacesByRoleAddsRolesQuery(t *testing.T) {
	// One roles=Admin call replaces a per-workspace role lookup, which is the
	// whole reason the flow can gate rename/delete for free.
	transport := stubTransport(t, seqResponse{status: 200, body: `{"value":[{"id":"` + testWSID + `","displayName":"DW - Finance"}]}`})

	got, err := ListWorkspacesByRole("tok", "Admin")
	if err != nil {
		t.Fatalf("ListWorkspacesByRole: %v", err)
	}
	if len(got) != 1 || got[0].DisplayName != "DW - Finance" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(transport.urls[0], "roles=Admin") {
		t.Errorf("url = %q, want a roles=Admin query", transport.urls[0])
	}
}

func TestListWorkspacesByRoleWithoutRolesFallsBackToPlainList(t *testing.T) {
	transport := stubTransport(t, seqResponse{status: 200, body: `{"value":[]}`})

	if _, err := ListWorkspacesByRole("tok", ""); err != nil {
		t.Fatalf("ListWorkspacesByRole: %v", err)
	}
	if strings.Contains(transport.urls[0], "roles=") {
		t.Errorf("url = %q, want no roles query when roles is empty", transport.urls[0])
	}
}

func TestGetWorkspaceDecodesCapacityAndDescription(t *testing.T) {
	transport := stubTransport(t, seqResponse{status: 200, body: `{
	  "id": "` + testWSID + `",
	  "displayName": "DW - Finance",
	  "description": "Finance data",
	  "type": "Workspace",
	  "capacityId": "` + testCapID + `"
	}`})

	ws, err := GetWorkspace("tok", testWSID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Description != "Finance data" || ws.CapacityID != testCapID || ws.Type != "Workspace" {
		t.Errorf("workspace = %+v", ws)
	}
	if transport.urls[0] != baseURL+"/v1/workspaces/"+testWSID {
		t.Errorf("url = %q", transport.urls[0])
	}
}

func TestListCapacitiesDecodesAndPages(t *testing.T) {
	transport := stubTransport(t,
		seqResponse{status: 200, body: `{
		  "value": [{"id":"` + testCapID + `","displayName":"F64 Capacity","sku":"F64","region":"Norway East","state":"Active"}],
		  "continuationUri": "` + baseURL + `/v1/capacities?continuationToken=abc"
		}`},
		seqResponse{status: 200, body: `{
		  "value": [{"id":"33333333-3333-3333-3333-333333333333","displayName":"F2 Capacity","sku":"F2","region":"West Europe","state":"Inactive"}]
		}`},
	)

	caps, err := ListCapacities("tok")
	if err != nil {
		t.Fatalf("ListCapacities: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("got %d capacities, want 2 (paging followed?)", len(caps))
	}
	if caps[0].SKU != "F64" || caps[0].Region != "Norway East" || caps[0].State != "Active" {
		t.Errorf("first capacity = %+v", caps[0])
	}
	if caps[1].State != "Inactive" {
		t.Errorf("second capacity state = %q", caps[1].State)
	}
	if len(transport.urls) != 2 {
		t.Errorf("expected 2 calls, got %v", transport.urls)
	}
}
