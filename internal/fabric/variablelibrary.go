package fabric

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SetVariableLibraryActiveSet switches which value set is active for a
// Variable Library in its workspace, via the item-specific update API. The
// active set is workspace state — not part of the item definition — so
// deploying a definition never changes it; this call is how a deploy enforces
// the value-set-per-environment convention after publishing.
func SetVariableLibraryActiveSet(token, workspaceID, itemID, valueSetName string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return err
	}
	body := struct {
		Properties struct {
			ActiveValueSetName string `json:"activeValueSetName"`
		} `json:"properties"`
	}{}
	body.Properties.ActiveValueSetName = valueSetName
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal variable-library update body: %w", err)
	}

	url := fmt.Sprintf("%s/v1/workspaces/%s/VariableLibraries/%s", baseURL, workspaceID, itemID)
	resp, respBody, err := doPatch(token, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("set active value set %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
