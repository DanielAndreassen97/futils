package deploy

import "regexp"

// pipelineGUID matches canonical GUIDs anywhere in a pipeline part. guidPat
// is the shared pattern from the semantic-model pass.
var pipelineGUID = regexp.MustCompile(guidPat)

// endpointHostRe matches a Fabric SQL analytics endpoint hostname, baked
// verbatim into a pipeline's Copy Activity / linked-service payload (unlike
// the semantic model's Sql.Database(...) form, pipelines carry the bare host
// string with no accompanying endpoint GUID to resolve by).
var endpointHostRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9-]*\.datawarehouse\.fabric\.microsoft\.com`)

// RebindPipeline rewrites baseline references in a DataPipeline part.
// Pipelines had no rebind pass at all before this: parameters and activity
// payloads carry baked workspace GUIDs, item GUIDs, and SQL endpoint hosts,
// and the items API stores them verbatim. Two reference classes rewrite here:
//   - baseline workspace GUIDs, via the consensus/seed workspace map
//     (workspaces are named per-env, so items vote — see buildWorkspaceMap)
//   - baseline item GUIDs, by name like every other pass
//
// SQL endpoint hosts are a later task's addition. Anything unrecognized in
// either loop (unknown GUID, ambiguous workspace, a resolved endpoint owner
// with no same-named lakehouse in the target) is left untouched: the
// leftover scan — not this pass — owns every unresolved-reference warning,
// so nothing is double-reported here.
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

	hostSeen := map[string]bool{} // avoids redundant lookups for a host repeated in this part
	for _, host := range endpointHostRe.FindAllString(string(content), -1) {
		if hostSeen[host] {
			continue
		}
		hostSeen[host] = true
		owner, ok := rb.baselineLakehouseByHost(host)
		if !ok {
			continue // not a baseline endpoint host — leftover scan owns it
		}
		tgtLake, ok := rb.target.ItemByName(owner.Name, "Lakehouse")
		if !ok {
			continue // no same-named lakehouse in the target — leftover scan owns it
		}
		tgtHost, _, ok := rb.targetEndpointFor(tgtLake)
		if !ok || tgtHost == host {
			continue
		}
		recordChangePair(&out, pairSeen, "SQL endpoint", owner.Name, host, tgtHost)
	}

	return []byte(applyChanges(string(content), out.Changes)), out
}
