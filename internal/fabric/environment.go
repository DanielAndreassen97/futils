package fabric

import (
	"encoding/json"
	"fmt"
)

// PublishEnvironment triggers the async staging→publish of an Environment
// item. Deploying an Environment's definition only STAGES its sparkcompute
// settings and libraries; nothing takes effect until this environment-specific
// publish runs. The server-side operation can take minutes (library
// resolution), so this only submits it — track progress with
// GetEnvironmentPublishState, mirroring fabric-cicd's split.
func PublishEnvironment(token, workspaceID, itemID string) error {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/environments/%s/staging/publish", baseURL, workspaceID, itemID)
	resp, respBody, err := doPost(token, url, nil)
	if err != nil {
		return err
	}
	// 200 or 202 both mean the publish was accepted; the operation itself is
	// tracked via the environment's publishDetails, not the LRO Location.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("publish environment %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// GetEnvironmentPublishState returns the environment's current
// properties.publishDetails.state — "running", "success", "failed",
// "cancelled" (case per API), or "" when the environment has never been
// published.
func GetEnvironmentPublishState(token, workspaceID, itemID string) (string, error) {
	if err := validateUUID(workspaceID, "workspace ID"); err != nil {
		return "", err
	}
	if err := validateUUID(itemID, "item ID"); err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/v1/workspaces/%s/environments/%s", baseURL, workspaceID, itemID)
	body, err := doGet(token, url)
	if err != nil {
		return "", err
	}
	var env struct {
		Properties struct {
			PublishDetails struct {
				State string `json:"state"`
			} `json:"publishDetails"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("parse environment: %w", err)
	}
	return env.Properties.PublishDetails.State, nil
}
