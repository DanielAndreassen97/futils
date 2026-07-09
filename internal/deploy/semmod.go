package deploy

import (
	"regexp"
	"strings"
)

// sqlDbRe matches a Direct Lake on SQL data-source expression, capturing the
// SQL analytics endpoint host (connection string) and its endpoint GUID:
// Sql.Database("<host>", "<endpoint-id>")
var sqlDbRe = regexp.MustCompile(`Sql\.Database\(\s*"([^"]+)"\s*,\s*"([^"]+)"\s*\)`)

// guidPat matches a canonical lowercase/upper GUID (used inside connection URLs).
const guidPat = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

// onelakeRe matches a Direct Lake on OneLake data-source URL, capturing the
// workspace GUID and the lakehouse/warehouse GUID:
// https://onelake.dfs.fabric.microsoft.com/<workspace>/<lakehouse>
// The workspace segment is captured as any non-slash sequence (production
// workspaces are GUIDs; test fixtures may use readable IDs).
var onelakeRe = regexp.MustCompile(`onelake\.dfs\.fabric\.microsoft\.com/([^/"]+)/(` + guidPat + `)`)

// targetEndpointFor returns the target lakehouse's SQL endpoint (host, id),
// cached by lakehouse GUID. The lock spans the check-and-fill so two workers
// can't both populate the cache; the memoClient dedups the underlying call.
func (rb *Rebinder) targetEndpointFor(lake IndexedItem) (string, string, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.targetEndpoint == nil {
		rb.targetEndpoint = map[string][2]string{}
	}
	if v, ok := rb.targetEndpoint[lake.GUID]; ok {
		return v[0], v[1], true
	}
	host, id, err := rb.client.GetLakehouseSqlEndpoint(rb.token, lake.WorkspaceID, lake.GUID)
	if err != nil || host == "" || id == "" {
		return "", "", false
	}
	rb.targetEndpoint[lake.GUID] = [2]string{host, id}
	return host, id, true
}

// rebindSQLSources rewrites every Direct Lake on SQL data-source expression in
// s: it looks up the baked endpoint id in the baseline name index (the baked
// GUID equals its parent lakehouse's sqlEndpointProperties.id, so it is
// indexed alongside every other item type — including SQLEndpoint items that
// aren't Lakehouses themselves), resolves the same-named target lakehouse,
// fetches that lakehouse's target endpoint, and replaces both host and id.
// Unresolvable endpoints are left unchanged and surfaced. Returns the
// rewritten string.
func (rb *Rebinder) rebindSQLSources(s string, out *RebindOutcome) string {
	seen := map[string]bool{}
	sqlStart := len(out.Changes)
	for _, m := range sqlDbRe.FindAllStringSubmatch(s, -1) {
		host, id := m[1], m[2]
		base, ok := rb.baseline.ItemByGUID(id)
		if !ok {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: ReasonNameUnknown})
			continue
		}
		tgt, st := rb.target.LookupName(base.Name, "Lakehouse")
		if st != LookupFound {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: reasonForStatus(st)})
			continue
		}
		newHost, newID, ok := rb.targetEndpointFor(tgt)
		if !ok {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: ReasonNotInTarget})
			continue
		}
		addChange(out, seen, "SQL endpoint", tgt.Name, host, newHost)
		addChange(out, seen, "SQL endpoint", tgt.Name, id, newID)
	}
	// Apply only THIS pass's changes: the OneLake pass rewrites per-URL, so its
	// recorded (display) changes must not be re-applied globally here — a
	// baseline workspace GUID can map to different targets per lakehouse, and a
	// global replace would also corrupt URLs left intact as unresolved.
	return applyChanges(s, out.Changes[sqlStart:])
}

// RebindSemanticModel rewrites a Direct Lake semantic model part's data-source
// connection from baseline to target. It handles both on-disk shapes: Direct
// Lake on OneLake (workspace+lakehouse GUID URL) and Direct Lake on SQL
// (Sql.Database host+endpoint-id). Only the values inside a matched connection
// expression are rewritten. Content with neither shape is returned unchanged.
func (rb *Rebinder) RebindSemanticModel(content []byte) ([]byte, RebindOutcome) {
	var out RebindOutcome
	s := string(content)
	s = rb.rebindOneLakeSources(s, &out)
	s = rb.rebindSQLSources(s, &out)
	return []byte(s), out
}

// rebindOneLakeSources rewrites every Direct Lake on OneLake source URL in s by
// resolving its lakehouse GUID baseline→target (by name, overrides honored) and
// its workspace GUID to the resolved lakehouse's target workspace. Each URL is
// rewritten as a WHOLE (workspace + lakehouse segment together): a global
// per-GUID replace cannot express two lakehouses that share a baseline
// workspace but resolve to different target workspaces, and would also touch
// URLs deliberately left intact as unresolved. Applied changes (per-GUID, for
// the summary) and unresolved refs are appended to out; the recorded changes
// are display-only here and are never re-applied globally.
func (rb *Rebinder) rebindOneLakeSources(s string, out *RebindOutcome) string {
	seenURL := map[string]bool{}
	seenChange := map[string]bool{}
	rewrites := map[string]string{} // full matched URL -> rewritten URL
	record := func(kind, name, oldV, newV string) {
		key := oldV + "\x00" + newV
		if oldV == "" || oldV == newV || seenChange[key] {
			return
		}
		seenChange[key] = true
		out.Changes = append(out.Changes, RebindChange{Kind: kind, Name: name, Old: oldV, New: newV})
	}
	for _, m := range onelakeRe.FindAllStringSubmatch(s, -1) {
		full, wsGUID, lhGUID := m[0], m[1], m[2]
		if seenURL[full] {
			continue
		}
		seenURL[full] = true
		it, ok, reason := rb.resolveGUIDReason(lhGUID)
		if !ok {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: lhGUID, ItemType: "Lakehouse", Location: "onelake source", Reason: reason})
			continue
		}
		newWS := wsGUID
		if it.WorkspaceID != "" {
			newWS = it.WorkspaceID
		}
		if newURL := "onelake.dfs.fabric.microsoft.com/" + newWS + "/" + it.GUID; newURL != full {
			rewrites[full] = newURL
		}
		record("Lakehouse", it.Name, lhGUID, it.GUID)
		if it.WorkspaceID != "" {
			record("Workspace", rb.workspaceName(it.WorkspaceID), wsGUID, it.WorkspaceID)
		}
	}
	for oldURL, newURL := range rewrites {
		s = strings.ReplaceAll(s, oldURL, newURL)
	}
	return s
}
