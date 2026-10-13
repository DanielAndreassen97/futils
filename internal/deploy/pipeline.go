package deploy

import (
	"regexp"
	"slices"
	"strings"
)

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
// Bare SQL endpoint hosts rewrite too, via targetHostFor. Anything
// unrecognized (unknown GUID, ambiguous workspace, a resolved endpoint owner
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
		if label, tgtHost, ok := rb.targetHostFor(host); ok {
			recordChangePair(&out, pairSeen, "SQL endpoint", label, host, tgtHost)
		}
	}

	return []byte(applyChanges(string(content), out.Changes)), out
}

// hostResolution is where a baseline SQL endpoint host lands in the target.
// A host belongs to a workspace, so it is resolved through every baseline
// lakehouse it serves: each is looked up by name in the target, and the
// target endpoints of those found are collected.
type hostResolution struct {
	owners      []IndexedItem // baseline lakehouses served by the host, by name
	resolved    []string      // owners whose same-named target lakehouse has an endpoint
	targets     []string      // distinct target endpoint hosts of resolved, sorted
	endpointErr bool          // a same-named target lakehouse exists but its endpoint lookup failed
}

func (rb *Rebinder) resolveHost(host string) hostResolution {
	r := hostResolution{owners: rb.baselineLakehousesByHost(host)}
	for _, owner := range r.owners {
		tgtLake, ok := rb.target.ItemByName(owner.Name, "Lakehouse")
		if !ok {
			continue
		}
		h, _, ok := rb.targetEndpointFor(tgtLake)
		if !ok {
			r.endpointErr = true
			continue
		}
		r.resolved = append(r.resolved, owner.Name)
		if !slices.Contains(r.targets, h) {
			r.targets = append(r.targets, h)
		}
	}
	slices.Sort(r.targets)
	return r
}

// targetHostFor maps a bare baseline SQL endpoint host to its target host.
// Owners missing from the target don't count against it, since any owner that
// is present shows where the workspace went. ok is false whenever there is
// nothing safe to rewrite: the host isn't a baseline endpoint, no owner
// resolves in the target, the owners land on different target endpoints, or
// both envs share the endpoint. Callers leave the host as-is then and stay
// silent; the leftover scan owns the warning. label names the resolved owners.
func (rb *Rebinder) targetHostFor(host string) (label, tgtHost string, ok bool) {
	r := rb.resolveHost(host)
	if len(r.targets) != 1 || r.targets[0] == host {
		return "", "", false
	}
	return strings.Join(r.resolved, ", "), r.targets[0], true
}
