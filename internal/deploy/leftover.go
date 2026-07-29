package deploy

import "fmt"

// LocationLeftover is the stable UnresolvedRef.Location every ScanLeftovers
// ref carries, regardless of which part file it was found in. AddUnresolved
// dedups on (GUID, ItemType, Location), so a single broken reference repeated
// across many part files of one item (a semantic model's 40 table
// expressions, say) collapses into one entry with Count summing the
// occurrences, instead of one line per part. The part path that would
// otherwise have gone in Location is prepended to Hint instead, so it still
// shows up (for the first occurrence) once printed.
const LocationLeftover = "leftover"

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
			hint := fmt.Sprintf("%s: workspace %q", partPath, name)
			if tgt, mapped := rb.wsMap[guid]; mapped {
				hint += fmt.Sprintf("; in the target this is %s", tgt)
			} else if rb.wsAmbiguous[guid] {
				hint += "; ambiguous (its items map to multiple target workspaces)"
			}
			refs = append(refs, UnresolvedRef{GUID: guid, ItemType: "Workspace", Location: LocationLeftover, Reason: ReasonLeftover, Hint: hint})
			continue
		}

		if base, ok := rb.baseline.ItemByGUID(guid); ok {
			it, resolved, reason := rb.resolveGUIDReason(guid, "")
			if resolved {
				if it.GUID == guid {
					continue // same item registered in both envs — correct on both sides
				}
				refs = append(refs, UnresolvedRef{GUID: guid, ItemType: base.Type, Location: LocationLeftover, Reason: ReasonLeftover,
					Hint: fmt.Sprintf("%s: %s %q; in the target this is %s", partPath, base.Type, base.Name, it.GUID)})
				continue
			}
			// The real cause distinguishes an ambiguous name (matches several
			// target workspaces, so name-matching is unsafe) from a name that's
			// simply absent from the target — reusing resolveGUIDReason's
			// discarded reason instead of always blaming absence.
			hint := fmt.Sprintf("%s: %s %q", partPath, base.Type, base.Name)
			if reason == ReasonAmbiguous {
				hint += "; ambiguous (the name matches items in multiple target workspaces)"
			} else {
				hint += "; no same-named item in the target"
			}
			refs = append(refs, UnresolvedRef{GUID: guid, ItemType: base.Type, Location: LocationLeftover, Reason: ReasonLeftover, Hint: hint})
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
		hint := fmt.Sprintf("%s: SQL endpoint of %s %q", partPath, owner.Type, owner.Name)
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
		refs = append(refs, UnresolvedRef{GUID: host, ItemType: "SQL endpoint", Location: LocationLeftover, Reason: ReasonLeftover, Hint: hint})
	}

	return refs
}
