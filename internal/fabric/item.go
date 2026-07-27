package fabric

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// MaxItemDescLen is Fabric's cap on an item description. Deliberately not the
// workspace limit: a workspace allows 4000 characters, an item 256, and reusing
// one constant for both would let a description through to a certain rejection.
const MaxItemDescLen = 256

// RenameItem sets an item's display name, leaving its description untouched.
//
// Split from SetItemDescription for the same reason RenameWorkspace is split
// from SetWorkspaceDescription: the PATCH accepts either field on its own, and
// a combined call would either carry a stale local description into the
// workspace or be unable to clear one. UpdateItem still sends both, which is
// correct for deploy — it knows both values from git.
func RenameItem(token, workspaceID, itemID, displayName string) (Item, error) {
	return patchItem(token, workspaceID, itemID, struct {
		DisplayName string `json:"displayName"`
	}{DisplayName: displayName})
}

// SetItemDescription replaces an item's description. An empty string clears it,
// so description is always sent.
func SetItemDescription(token, workspaceID, itemID, description string) (Item, error) {
	return patchItem(token, workspaceID, itemID, struct {
		Description string `json:"description"`
	}{Description: description})
}

// patchItem issues the shared PATCH /v1/workspaces/{ws}/items/{id} and decodes
// the updated item.
func patchItem(token, workspaceID, itemID string, body any) (Item, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return Item{}, err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return Item{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Item{}, fmt.Errorf("marshal update-item body: %w", err)
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/items/%s", baseURL, workspaceID, itemID)
	resp, respBody, err := doPatch(token, url, bytes.NewReader(payload))
	if err != nil {
		return Item{}, err
	}
	if resp.StatusCode >= 400 {
		return Item{}, itemWriteError(resp.StatusCode, respBody)
	}
	// A 200 with no body still means the update landed. UpdateItem — which the
	// deploy flow uses to push descriptions — ignores the returned item entirely,
	// so refusing an empty body here would turn a successful metadata update into
	// a failed deploy.
	if len(respBody) == 0 {
		return Item{}, nil
	}
	var it Item
	if err := json.Unmarshal(respBody, &it); err != nil {
		return Item{}, fmt.Errorf("parse updated item: %w", err)
	}
	return it, nil
}

// itemWriteError turns a failed item PATCH into something the user can act on.
// The Update Item API needs read AND write permission on the item, and Fabric's
// 403 body says only "InsufficientPrivileges".
func itemWriteError(status int, body []byte) error {
	if status == http.StatusForbidden {
		return fmt.Errorf("update item %d: needs read and write permission on the item: %s", status, string(body))
	}
	return fmt.Errorf("update item %d: %s", status, string(body))
}
