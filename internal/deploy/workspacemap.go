package deploy

// buildWorkspaceMap derives baseline→target workspace pairs by consensus:
// every baseline item that resolves by name+type in the target casts one vote
// (its baseline workspace → the resolved item's target workspace). A baseline
// workspace whose votes all agree maps; conflicting votes mark it ambiguous
// (no rewrite, surfaced as unresolved on use). Workspaces are named
// differently per environment, so name lookup — the item mechanism — cannot
// apply to them directly; their items vote instead.
func buildWorkspaceMap(baseline, target *NameIndex) (map[string]string, map[string]bool) {
	wsMap := map[string]string{}
	ambiguous := map[string]bool{}
	for _, it := range baseline.byGUID {
		tgt, ok := target.ItemByName(it.Name, it.Type)
		if !ok || it.WorkspaceID == "" || tgt.WorkspaceID == "" {
			continue
		}
		if ambiguous[it.WorkspaceID] {
			continue
		}
		if prev, seen := wsMap[it.WorkspaceID]; seen && prev != tgt.WorkspaceID {
			delete(wsMap, it.WorkspaceID)
			ambiguous[it.WorkspaceID] = true
			continue
		}
		wsMap[it.WorkspaceID] = tgt.WorkspaceID
	}
	return wsMap, ambiguous
}

// SetWorkspaceSeeds installs explicit baseline→target workspace pairs (from
// the cmd layer's deploy-mapping folder join). Seeds are authoritative: they
// overwrite consensus results and clear ambiguity for their keys, and they
// cover workspaces that hold no items to vote with.
func (rb *Rebinder) SetWorkspaceSeeds(seeds map[string]string) {
	if rb.wsMap == nil {
		rb.wsMap = map[string]string{}
	}
	for b, t := range seeds {
		rb.wsMap[b] = t
		delete(rb.wsAmbiguous, b)
	}
}
