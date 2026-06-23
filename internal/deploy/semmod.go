package deploy

import "regexp"

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

// ensureBaseEndpoints lazily builds baseline { SQL-endpoint id -> lakehouse }
// by querying GetLakehouseSqlEndpoint for every baseline lakehouse. Lakehouses
// whose endpoint can't be fetched (e.g. still provisioning) are skipped.
// Caller must hold rb.mu.
func (rb *Rebinder) ensureBaseEndpoints() {
	if rb.baseEndpoints != nil {
		return
	}
	rb.baseEndpoints = map[string]IndexedItem{}
	for _, lake := range rb.baseline.ItemsOfType("Lakehouse") {
		_, id, err := rb.client.GetLakehouseSqlEndpoint(rb.token, lake.WorkspaceID, lake.GUID)
		if err != nil || id == "" {
			continue
		}
		rb.baseEndpoints[id] = lake
	}
}

// baseEndpointLookup returns the baseline lakehouse for a SQL-endpoint id,
// populating the cache on first use. Guards the lazy fill + read so it's safe
// to call from concurrent compare workers.
func (rb *Rebinder) baseEndpointLookup(id string) (IndexedItem, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.ensureBaseEndpoints()
	lake, ok := rb.baseEndpoints[id]
	return lake, ok
}

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
// s: it maps the baked endpoint id to its baseline lakehouse, resolves the
// same-named target lakehouse, fetches that lakehouse's target endpoint, and
// replaces both host and id. Unresolvable endpoints are left unchanged and
// surfaced. Returns the rewritten string.
func (rb *Rebinder) rebindSQLSources(s string, out *RebindOutcome) string {
	seen := map[string]bool{}
	for _, m := range sqlDbRe.FindAllStringSubmatch(s, -1) {
		host, id := m[1], m[2]
		lake, ok := rb.baseEndpointLookup(id)
		if !ok {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: ReasonNameUnknown})
			continue
		}
		tgt, st := rb.target.LookupName(lake.Name, "Lakehouse")
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
	// out.Changes may include entries appended by a prior pass (OneLake);
	// applyChanges over a value no longer present is a harmless no-op.
	return applyChanges(s, out.Changes)
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
// its workspace GUID to the resolved lakehouse's target workspace. Applied
// changes and unresolved refs are appended to out. Returns the rewritten string.
func (rb *Rebinder) rebindOneLakeSources(s string, out *RebindOutcome) string {
	seen := map[string]bool{}
	for _, m := range onelakeRe.FindAllStringSubmatch(s, -1) {
		wsGUID, lhGUID := m[1], m[2]
		if it, ok, reason := rb.resolveGUIDReason(lhGUID); ok {
			addChange(out, seen, "Lakehouse", it.Name, lhGUID, it.GUID)
			if it.WorkspaceID != "" {
				addChange(out, seen, "Workspace", rb.workspaceName(it.WorkspaceID), wsGUID, it.WorkspaceID)
			}
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: lhGUID, ItemType: "Lakehouse", Location: "onelake source", Reason: reason})
		}
	}
	return applyChanges(s, out.Changes)
}
