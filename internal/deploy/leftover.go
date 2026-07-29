package deploy

import "fmt"

// ScanLeftovers reports every GUID or SQL-endpoint host in a finished part
// that still matches the baseline environment. It runs AFTER every rewrite
// pass, so anything it finds either sits where no pass rewrites (arbitrary
// notebook/report content is deliberately never auto-rewritten — the value
// may be documentation or intentional) or failed to resolve. Warn-only by
// design: a rewrite must know what a value means; a warning only needs to
// know it is wrong. Identity values (shared reference workspaces, items
// registered in both envs under the same GUID) are skipped — they are
// correct on both sides. Never mutates content.
func (rb *Rebinder) ScanLeftovers(partPath string, content []byte) []UnresolvedRef {
	var refs []UnresolvedRef
	s := string(content)
	seen := map[string]bool{}

	for _, guid := range pipelineGUID.FindAllString(s, -1) {
		if seen[guid] {
			continue
		}
		seen[guid] = true

		if name, ok := rb.baselineWSNames[guid]; ok {
			if rb.wsMap[guid] == guid {
				continue // shared workspace, correct on both sides
			}
			hint := fmt.Sprintf("workspace %q", name)
			if tgt, mapped := rb.wsMap[guid]; mapped {
				hint += fmt.Sprintf("; in the target this is %s", tgt)
			} else if rb.wsAmbiguous[guid] {
				hint += "; ambiguous (its items map to multiple target workspaces)"
			}
			refs = append(refs, UnresolvedRef{GUID: guid, ItemType: "Workspace", Location: partPath, Reason: ReasonLeftover, Hint: hint})
			continue
		}

		if base, ok := rb.baseline.ItemByGUID(guid); ok {
			if it, resolved, _ := rb.resolveGUIDReason(guid, ""); resolved {
				if it.GUID == guid {
					continue // same item registered in both envs — correct on both sides
				}
				refs = append(refs, UnresolvedRef{GUID: guid, ItemType: base.Type, Location: partPath, Reason: ReasonLeftover,
					Hint: fmt.Sprintf("%s %q; in the target this is %s", base.Type, base.Name, it.GUID)})
				continue
			}
			refs = append(refs, UnresolvedRef{GUID: guid, ItemType: base.Type, Location: partPath, Reason: ReasonLeftover,
				Hint: fmt.Sprintf("%s %q; no same-named item in the target", base.Type, base.Name)})
			continue
		}
		// Unknown GUID: not the baseline's — silently ignored, may belong to
		// neither env (e.g. a third-party connector id).
	}

	for _, host := range endpointHostRe.FindAllString(s, -1) {
		if seen[host] {
			continue
		}
		seen[host] = true

		// baselineLakehouseByHost lazily builds the reverse host map on first
		// call — only reached here, when a host actually matched the SQL
		// endpoint pattern, so content with no hardcoded hosts triggers zero
		// extra API calls.
		owner, ok := rb.baselineLakehouseByHost(host)
		if !ok {
			continue
		}
		hint := fmt.Sprintf("SQL endpoint of %s %q", owner.Type, owner.Name)
		if tgtLake, ok := rb.target.ItemByName(owner.Name, "Lakehouse"); ok {
			if tgtHost, _, ok := rb.targetEndpointFor(tgtLake); ok {
				if tgtHost == host {
					continue // shared endpoint, correct on both sides
				}
				hint += fmt.Sprintf("; in the target this is %s", tgtHost)
			} else {
				hint += "; couldn't resolve the target lakehouse's SQL endpoint"
			}
		} else {
			hint += "; no same-named lakehouse in the target"
		}
		refs = append(refs, UnresolvedRef{GUID: host, ItemType: "SQL endpoint", Location: partPath, Reason: ReasonLeftover, Hint: hint})
	}

	return refs
}
