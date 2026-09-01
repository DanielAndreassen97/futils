package deploy

import "regexp"

// zeroWorkspaceRe matches Fabric's all-zeros GUID only where it is the value
// of a workspace-reference key: a pipeline activity's "workspaceId", a
// notebook's "default_lakehouse_workspace_id" (JSON or the .py META form), or
// a bare "workspace". The key set and shape are fabric-cicd's
// WORKSPACE_ID_REFERENCE_REGEX, so the two tools agree on what the zero GUID
// means. Group 1 is everything up to and including the opening quote of the
// value, kept verbatim so formatting survives.
var zeroWorkspaceRe = regexp.MustCompile(`("?(?:default_lakehouse_workspace_id|workspaceId|workspace)"?\s*[:=]\s*")` + placeholderGUID + `"`)

// rebindZeroWorkspace resolves the all-zeros GUID Fabric's git export writes
// for "the workspace this item lives in". Git-sync resolves it on import, but
// the items API stores it verbatim, so an activity would point at a workspace
// that does not exist. It becomes targetWS — the workspace the item deploys
// into — but ONLY behind a workspace key: the same value in a pipeline
// parameter default, a code cell, or a shortcut's itemId (a self-reference to
// the lakehouse) means something else and is left alone. Recorded once as a
// Workspace change so the summary shows it. Empty targetWS is a no-op.
func (rb *Rebinder) rebindZeroWorkspace(content []byte, targetWS string) ([]byte, RebindOutcome) {
	var out RebindOutcome
	if targetWS == "" || !zeroWorkspaceRe.Match(content) {
		return content, out
	}
	rewritten := zeroWorkspaceRe.ReplaceAll(content, []byte(`${1}`+targetWS+`"`))
	recordChangePair(&out, map[string]bool{}, "Workspace", rb.workspaceName(targetWS), placeholderGUID, targetWS)
	return rewritten, out
}
