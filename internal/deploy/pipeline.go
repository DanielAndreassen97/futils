package deploy

import "regexp"

// pipelineGUID matches canonical GUIDs anywhere in a pipeline part. guidPat
// is the shared pattern from the semantic-model pass.
var pipelineGUID = regexp.MustCompile(guidPat)

// RebindPipeline rewrites baseline references in a DataPipeline part.
// Pipelines had no rebind pass at all before this: parameters and activity
// payloads carry baked workspace GUIDs, item GUIDs, and SQL endpoint hosts,
// and the items API stores them verbatim. Two reference classes rewrite here:
//   - baseline workspace GUIDs, via the consensus/seed workspace map
//     (workspaces are named per-env, so items vote — see buildWorkspaceMap)
//   - baseline item GUIDs, by name like every other pass
//
// SQL endpoint hosts are a later task's addition. Anything unrecognized
// (unknown GUID, ambiguous workspace) is left untouched: the leftover scan —
// not this pass — owns warnings, so nothing is double-reported here.
func (rb *Rebinder) RebindPipeline(content []byte) ([]byte, RebindOutcome) {
	var out RebindOutcome
	pairSeen := map[string]bool{}
	for _, guid := range pipelineGUID.FindAllString(string(content), -1) {
		if tgt, ok := rb.wsMap[guid]; ok {
			recordChangePair(&out, pairSeen, "Workspace", rb.workspaceName(tgt), guid, tgt)
			continue
		}
		if it, ok, _ := rb.resolveGUIDReason(guid, ""); ok {
			recordChangePair(&out, pairSeen, it.Type, it.Name, guid, it.GUID)
		}
	}
	return []byte(applyChanges(string(content), out.Changes)), out
}
