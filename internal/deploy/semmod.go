package deploy

import (
	"regexp"
	"sort"
	"strings"
)

// sqlDbRe matches a Direct Lake on SQL data-source expression, capturing the
// SQL analytics endpoint host (connection string) and its endpoint GUID:
// Sql.Database("<host>", "<endpoint-id>")
var sqlDbRe = regexp.MustCompile(`Sql\.Database\(\s*"([^"]+)"\s*,\s*"([^"]+)"\s*\)`)

// guidShapeRe tells the two Sql.Database second-argument forms apart: a
// UUID-shaped value is a baked SQL-endpoint id, anything else is the database
// NAME (e.g. "LH_Gold").
var guidShapeRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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

// baselineLakehouseByHost resolves a SQL endpoint host to the baseline
// lakehouse that owns it. The reverse map is built lazily on the first call —
// one GetLakehouseSqlEndpoint per baseline lakehouse — so deploys whose
// content carries no hardcoded hosts pay zero extra API calls. Fetch errors
// skip that lakehouse: resolution is best-effort and the leftover scan will
// still flag the host as baseline-looking if it matches nothing.
func (rb *Rebinder) baselineLakehouseByHost(host string) (IndexedItem, bool) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.baselineHostMap == nil {
		rb.baselineHostMap = map[string]IndexedItem{}
		for _, it := range rb.baseline.byGUID {
			if it.Type != "Lakehouse" {
				continue
			}
			h, _, err := rb.client.GetLakehouseSqlEndpoint(rb.token, it.WorkspaceID, it.GUID)
			if err != nil || h == "" {
				continue
			}
			rb.baselineHostMap[h] = it
		}
	}
	it, ok := rb.baselineHostMap[host]
	return it, ok
}

// resolveDBNameReason resolves a SQL database name (the NAME form of
// Sql.Database's second argument) to the target lakehouse that owns the
// endpoint. Overrides are consulted first and exactly, like every other rebind
// pass; the index lookup then falls back to case folding via LookupNameFold,
// because SQL database names are case-insensitive while Fabric item display
// names in the index are stored as typed. Returns the lookup reason on a miss.
func (rb *Rebinder) resolveDBNameReason(name string) (IndexedItem, bool, string) {
	if ov, ok := rb.overrides[name]; ok {
		it, st := rb.target.LookupName(ov.ItemName, ov.ItemType)
		return it, st == LookupFound, reasonForStatus(st)
	}
	it, st := rb.target.LookupNameFold(name, "Lakehouse")
	return it, st == LookupFound, reasonForStatus(st)
}

// rebindSQLSources rewrites every Direct Lake on SQL data-source expression in
// s: it resolves the baked endpoint id (the baked GUID equals its parent
// lakehouse's sqlEndpointProperties.id, so it is indexed alongside every other
// item type — including SQLEndpoint items that aren't Lakehouses themselves)
// through resolveGUIDReason with target type "Lakehouse" — that consults
// rb.overrides FIRST, exactly like every other rebind pass, so a customer can
// register an override for a SQL-endpoint GUID that the baseline index can't
// otherwise place — fetches the resolved lakehouse's target endpoint, and
// replaces both host and id.
//
// The second argument also appears in NAME form (Sql.Database("host",
// "LH_Gold")): the database name equals the lakehouse name, is the same in the
// target, and involves no baseline lookup — the name resolves directly in the
// target index (override-first, then case-insensitively) and only the host is
// rewritten; the name literal itself is never touched.
//
// Like rebindOneLakeSources, each match is rewritten by byte SPAN, not by a
// global ReplaceAll of the extracted host/id values: two Sql.Database(...)
// expressions can share one baseline HOST but resolve to two different target
// hosts (their ids differ, so their spans differ too), and a global replace of
// the shared old host would funnel both into whichever target resolved first.
// Rewriting per-match span means only that expression's own host/id substrings
// are touched; an unresolved expression's span is copied through untouched
// even if it happens to share a host with a resolved one.
func (rb *Rebinder) rebindSQLSources(s string, out *RebindOutcome) string {
	locs := sqlDbRe.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	seenChange := map[string]bool{}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		hostStart, hostEnd := loc[2], loc[3]
		idStart, idEnd := loc[4], loc[5]
		host, id := s[hostStart:hostEnd], s[idStart:idEnd]

		if !guidShapeRe.MatchString(id) {
			// Name form: Sql.Database("host", "LH_Gold") carries the database
			// NAME, not an endpoint GUID. The name is the same in the target, so
			// resolve it there directly and rewrite only the host. The lookup
			// ignores case (SQL does too), but the literal is copied through
			// byte-for-byte: Fabric binds a model's data source to its cloud
			// connection by exact string match, so the casing in git is the
			// casing the customer's connection was made for.
			tgt, ok, reason := rb.resolveDBNameReason(id)
			if !ok {
				out.AddUnresolved(UnresolvedRef{GUID: id, ItemType: "SQL database (by name)", Location: "Sql.Database", Reason: reason})
				continue
			}
			newHost, _, ok := rb.targetEndpointFor(tgt)
			if !ok {
				out.AddUnresolved(UnresolvedRef{GUID: id, ItemType: "SQL database (by name)", Location: "Sql.Database", Reason: ReasonNotInTarget})
				continue
			}
			b.WriteString(s[last:hostStart])
			b.WriteString(newHost)
			last = hostEnd
			recordChangePair(out, seenChange, "SQL endpoint", tgt.Name, host, newHost)
			continue
		}

		tgt, ok, reason := rb.resolveGUIDReason(id, "Lakehouse")
		if !ok {
			out.AddUnresolved(UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: reason})
			continue
		}
		newHost, newID, ok := rb.targetEndpointFor(tgt)
		if !ok {
			out.AddUnresolved(UnresolvedRef{GUID: id, ItemType: "SQL endpoint", Location: "Sql.Database", Reason: ReasonNotInTarget})
			continue
		}

		b.WriteString(s[last:hostStart])
		b.WriteString(newHost)
		b.WriteString(s[hostEnd:idStart])
		b.WriteString(newID)
		last = idEnd

		recordChangePair(out, seenChange, "SQL endpoint", tgt.Name, host, newHost)
		recordChangePair(out, seenChange, "SQL endpoint", tgt.Name, id, newID)
	}
	b.WriteString(s[last:])
	return b.String()
}

// RebindSemanticModel rewrites a Direct Lake semantic model part's data-source
// connection from baseline to target. It handles both on-disk shapes: Direct
// Lake on OneLake (workspace+lakehouse GUID URL) and Direct Lake on SQL
// (Sql.Database host+endpoint-id), plus a SQL endpoint host held in an M
// parameter that Sql.Database reads by name. Only the values inside a matched
// connection expression are rewritten. Content with none of these shapes is
// returned unchanged.
func (rb *Rebinder) RebindSemanticModel(content []byte) ([]byte, RebindOutcome) {
	return rb.rebindSemanticModel(content, nil)
}

// rebindSemanticModel is RebindSemanticModel with the model's other parts at
// hand: a parameter lives in expressions.tmdl, but the Sql.Database calls that
// say which database it serves sit in the table parts.
func (rb *Rebinder) rebindSemanticModel(content []byte, parts []Part) ([]byte, RebindOutcome) {
	var out RebindOutcome
	s := string(content)
	s = rb.rebindOneLakeSources(s, &out)
	s = rb.rebindSQLSources(s, &out)
	s = rb.rebindHostParameters(s, &out, parts)
	return []byte(s), out
}

// paramExprRe matches a single-line TMDL M parameter expression, capturing its
// name, its string-literal value and its meta record:
// expression SqlEndpoint = "<host>" meta [IsParameterQuery=true, ...]
// Anchored at line start, so /// descriptions and // comments that mention the
// same text never match.
var paramExprRe = regexp.MustCompile(`(?m)^[ \t]*expression[ \t]+('(?:[^']|'')*'|[^\s=]+)[ \t]*=[ \t]*"([^"]*)"[ \t]*meta[ \t]*\[([^\]]*)\]`)

// isParamQueryRe tells an M parameter's meta record from any other meta.
var isParamQueryRe = regexp.MustCompile(`(?i)\bIsParameterQuery\s*=\s*true\b`)

// wholeHostRe is endpointHostRe anchored to the whole value.
var wholeHostRe = regexp.MustCompile(`^` + endpointHostRe.String() + `$`)

// sqlDbParamRe matches Sql.Database called with an M identifier (a parameter)
// for the host and a literal database name, capturing both:
// Sql.Database(SqlEndpoint, "LH_Gold") or Sql.Database(#"Sql Endpoint", "LH_Gold")
var sqlDbParamRe = regexp.MustCompile(`Sql\.Database\(\s*(#"(?:[^"]|"")*"|[A-Za-z_][A-Za-z0-9_]*)\s*,\s*"([^"]+)"\s*\)`)

// paramDatabases maps each parameter passed as Sql.Database's host, across all
// of a model's parts, to the database names it is used with. Names that differ
// only in case count once, since SQL database names ignore case.
func paramDatabases(parts []Part) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, p := range parts {
		for _, m := range sqlDbParamRe.FindAllStringSubmatch(string(p.Content), -1) {
			param := m[1]
			if strings.HasPrefix(param, "#") {
				param = strings.ReplaceAll(param[2:len(param)-1], `""`, `"`)
			}
			key := param + "\x00" + strings.ToLower(m[2])
			if seen[key] {
				continue
			}
			seen[key] = true
			out[param] = append(out[param], m[2])
		}
	}
	for _, dbs := range out {
		sort.Strings(dbs)
	}
	return out
}

// tmdlName strips TMDL's single-quote quoting from an object name.
func tmdlName(name string) string {
	if len(name) >= 2 && strings.HasPrefix(name, "'") && strings.HasSuffix(name, "'") {
		return strings.ReplaceAll(name[1:len(name)-1], "''", "'")
	}
	return name
}

// targetHostForDatabases maps a baseline SQL endpoint host to the target host
// of the lakehouses the model reads through it, named by Sql.Database's
// database argument. Every lakehouse in a workspace shares one endpoint host,
// so the host alone can't say which lakehouse is meant; the database names
// can. ok is false when the host isn't a baseline endpoint, a database doesn't
// resolve in the target, the databases resolve to different target hosts, or
// the endpoint is shared; the leftover scan then reports what is left. label
// names the resolved target lakehouses.
func (rb *Rebinder) targetHostForDatabases(host string, dbs []string) (label, tgtHost string, ok bool) {
	if _, ok := rb.baselineLakehouseByHost(host); !ok {
		return "", "", false
	}
	var names []string
	for _, db := range dbs {
		tgt, found, _ := rb.resolveDBNameReason(db)
		if !found {
			return "", "", false
		}
		h, _, found := rb.targetEndpointFor(tgt)
		if !found || (tgtHost != "" && h != tgtHost) {
			return "", "", false
		}
		tgtHost = h
		names = append(names, tgt.Name)
	}
	if tgtHost == host {
		return "", "", false
	}
	return strings.Join(names, ", "), tgtHost, true
}

// rebindHostParameters rewrites a baseline SQL endpoint host held in an M
// parameter (IsParameterQuery=true) whose whole value is the host. Tables then
// read Sql.Database(SqlEndpoint, "LH_Gold"), which rebindSQLSources can't see
// since the host isn't a literal there. The target host comes from the
// databases the parameter is used with in parts (targetHostForDatabases); a
// parameter no Sql.Database call uses falls back to the host's baseline owner,
// like in a pipeline (targetHostFor). Only the parameter's value span is
// touched; the same host anywhere else (descriptions, comments, other expressions) is left
// alone, and an unresolvable host is left for the leftover scan to report.
func (rb *Rebinder) rebindHostParameters(s string, out *RebindOutcome, parts []Part) string {
	locs := paramExprRe.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var dbsByParam map[string][]string // built on the first candidate only
	seenChange := map[string]bool{}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		valStart, valEnd := loc[4], loc[5]
		host := s[valStart:valEnd]
		if !isParamQueryRe.MatchString(s[loc[6]:loc[7]]) || !wholeHostRe.MatchString(host) {
			continue
		}
		if dbsByParam == nil {
			dbsByParam = paramDatabases(parts)
		}
		var label, tgtHost string
		var ok bool
		if dbs := dbsByParam[tmdlName(s[loc[2]:loc[3]])]; len(dbs) > 0 {
			label, tgtHost, ok = rb.targetHostForDatabases(host, dbs)
		} else {
			var owner IndexedItem
			owner, tgtHost, ok = rb.targetHostFor(host)
			label = owner.Name
		}
		if !ok {
			continue
		}
		b.WriteString(s[last:valStart])
		b.WriteString(tgtHost)
		last = valEnd
		recordChangePair(out, seenChange, "SQL endpoint", label, host, tgtHost)
	}
	b.WriteString(s[last:])
	return b.String()
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
	for _, m := range onelakeRe.FindAllStringSubmatch(s, -1) {
		full, wsGUID, lhGUID := m[0], m[1], m[2]
		if seenURL[full] {
			continue
		}
		seenURL[full] = true
		it, ok, reason := rb.resolveGUIDReason(lhGUID, "")
		if !ok {
			out.AddUnresolved(UnresolvedRef{GUID: lhGUID, ItemType: "Lakehouse", Location: "onelake source", Reason: reason})
			continue
		}
		newWS := wsGUID
		if it.WorkspaceID != "" {
			newWS = it.WorkspaceID
		}
		if newURL := "onelake.dfs.fabric.microsoft.com/" + newWS + "/" + it.GUID; newURL != full {
			rewrites[full] = newURL
		}
		recordChangePair(out, seenChange, "Lakehouse", it.Name, lhGUID, it.GUID)
		if it.WorkspaceID != "" {
			recordChangePair(out, seenChange, "Workspace", rb.workspaceName(it.WorkspaceID), wsGUID, it.WorkspaceID)
		}
	}
	for oldURL, newURL := range rewrites {
		s = strings.ReplaceAll(s, oldURL, newURL)
	}
	return s
}
