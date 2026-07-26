package fabric

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Capacity is a Fabric capacity the caller can administer or contribute to.
// State is "Active" or "Inactive" — items cannot run on an inactive capacity,
// so the create flow flags those instead of hiding them.
type Capacity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	SKU         string `json:"sku"`
	Region      string `json:"region"`
	State       string `json:"state"`
}

// maxWorkspaceNameLen and maxWorkspaceDescLen are Fabric's documented limits.
// Enforced client-side so a typo costs no round trip.
const (
	MaxWorkspaceNameLen = 256
	MaxWorkspaceDescLen = 4000
	// ReservedWorkspaceName is rejected by Fabric on create and update.
	ReservedWorkspaceName = "Admin monitoring"
)

// CreateWorkspace creates a workspace and returns it. description and
// capacityID are optional: both are omitted from the payload when empty,
// because Fabric rejects an empty string where it expects a uuid.
//
// A workspace created without a capacity exists but cannot host Fabric items
// until a capacity is assigned — callers should say so.
func CreateWorkspace(token, displayName, description, capacityID string) (Workspace, error) {
	if capacityID != "" {
		if err := validateUUID(capacityID, "capacity ID"); err != nil {
			return Workspace{}, err
		}
	}
	body := struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description,omitempty"`
		CapacityID  string `json:"capacityId,omitempty"`
	}{DisplayName: displayName, Description: description, CapacityID: capacityID}
	payload, err := json.Marshal(body)
	if err != nil {
		return Workspace{}, fmt.Errorf("marshal create-workspace body: %w", err)
	}
	resp, respBody, err := doPost(token, baseURL+"/v1/workspaces", bytes.NewReader(payload))
	if err != nil {
		return Workspace{}, err
	}
	if resp.StatusCode >= 400 {
		return Workspace{}, fmt.Errorf("create workspace %q %d: %s", displayName, resp.StatusCode, string(respBody))
	}
	var ws Workspace
	if err := json.Unmarshal(respBody, &ws); err != nil {
		return Workspace{}, fmt.Errorf("parse created workspace: %w", err)
	}
	return ws, nil
}

// RenameWorkspace sets a workspace's display name, leaving its description
// untouched. Split from SetWorkspaceDescription on purpose: a single
// two-string Update would either overwrite the description with a stale local
// copy or be unable to clear it, and neither failure is obvious at the call
// site. Requires the Admin workspace role.
func RenameWorkspace(token, workspaceID, displayName string) (Workspace, error) {
	return patchWorkspace(token, workspaceID, struct {
		DisplayName string `json:"displayName"`
	}{DisplayName: displayName})
}

// SetWorkspaceDescription replaces a workspace's description. An empty string
// clears it, so description is always sent. Requires the Admin workspace role.
func SetWorkspaceDescription(token, workspaceID, description string) (Workspace, error) {
	return patchWorkspace(token, workspaceID, struct {
		Description string `json:"description"`
	}{Description: description})
}

// patchWorkspace issues the shared PATCH /v1/workspaces/{id} and decodes the
// updated workspace.
func patchWorkspace(token, workspaceID string, body any) (Workspace, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return Workspace{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Workspace{}, fmt.Errorf("marshal update-workspace body: %w", err)
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s", baseURL, workspaceID)
	resp, respBody, err := doPatch(token, url, bytes.NewReader(payload))
	if err != nil {
		return Workspace{}, err
	}
	if resp.StatusCode >= 400 {
		return Workspace{}, workspaceWriteError("update workspace", resp.StatusCode, respBody)
	}
	var ws Workspace
	if err := json.Unmarshal(respBody, &ws); err != nil {
		return Workspace{}, fmt.Errorf("parse updated workspace: %w", err)
	}
	return ws, nil
}

// DeleteWorkspace deletes a workspace AND every item under it. Requires the
// Admin workspace role. Irreversible — callers must confirm first.
func DeleteWorkspace(token, workspaceID string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s", baseURL, workspaceID)
	resp, respBody, err := doDelete(token, url)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return workspaceWriteError("delete workspace", resp.StatusCode, respBody)
	}
	return nil
}

// workspaceWriteError turns a failed PATCH/DELETE into a message the user can
// act on. Both operations require the Admin workspace role, and Fabric's 403
// body says only "InsufficientPrivileges" — the flow pre-checks the role, but a
// stale check must not leave the user guessing.
func workspaceWriteError(what string, status int, body []byte) error {
	if status == http.StatusForbidden {
		return fmt.Errorf("%s %d: requires the Admin workspace role: %s", what, status, string(body))
	}
	return fmt.Errorf("%s %d: %s", what, status, string(body))
}

// GetWorkspace returns one workspace including its description and capacity —
// fields the list endpoint also carries but which futils' list callers ignore.
func GetWorkspace(token, workspaceID string) (Workspace, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return Workspace{}, err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s", baseURL, workspaceID)
	body, err := doGet(token, url)
	if err != nil {
		return Workspace{}, err
	}
	var ws Workspace
	if err := json.Unmarshal(body, &ws); err != nil {
		return Workspace{}, fmt.Errorf("parse workspace: %w", err)
	}
	return ws, nil
}

// ListWorkspacesByRole returns the workspaces where the caller holds one of the
// given roles (comma-separated, e.g. "Admin"). One call answers "which of these
// can I rename or delete?" for the whole tenant — the alternative is a
// roleAssignments lookup per workspace. An empty roles string lists everything,
// same as ListWorkspaces.
func ListWorkspacesByRole(token, roles string) ([]Workspace, error) {
	startURL := baseURL + "/v1/workspaces"
	if roles != "" {
		startURL += "?roles=" + url.QueryEscape(roles)
	}
	return pagedGet[Workspace](token, startURL, "workspaces")
}

// ListCapacities returns the capacities the caller administers or contributes
// to. Needs the Capacity.Read.All scope, which the Azure CLI public client
// normally covers — callers should degrade gracefully if it doesn't.
func ListCapacities(token string) ([]Capacity, error) {
	return pagedGet[Capacity](token, baseURL+"/v1/capacities", "capacities")
}
