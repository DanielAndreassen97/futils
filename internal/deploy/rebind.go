package deploy

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Override is a futils-native reference override resolved by name, keyed by the
// baseline GUID as it appears in git. It mirrors config.ReferenceOverride
// without the deploy package depending on config — the cmd layer converts.
type Override struct {
	ItemType string
	ItemName string
}

// UnresolvedRef is a baseline GUID the rebinder could not translate. Surfaced
// to the user so they can register an override (or ignore/strip it). ItemName
// is the notebook the GUID was found in, filled in by the substitution pass.
type UnresolvedRef struct {
	GUID     string
	ItemType string
	Location string // "default_lakehouse" | "known_lakehouses"
	ItemName string
	Reason   string // ReasonNameUnknown | ReasonNotInTarget | ReasonAmbiguous
	// Count is how many occurrences collapsed into this ref (a model can carry
	// the same broken reference in every table expression). 0 means 1 — set
	// only by AddUnresolved.
	Count int
}

const (
	ReasonNameUnknown = "name-unknown"  // baseline GUID not in baseline index — no name to match by
	ReasonNotInTarget = "not-in-target" // name known but absent from every registered target workspace
	ReasonAmbiguous   = "ambiguous"     // name appears in 2+ target workspaces
)

// AddUnresolved records an unresolved reference on the outcome, deduplicated
// on (GUID, ItemType, Location): a model carrying the same broken reference in
// 80 table expressions reports it once with Count=80, not as 80 identical
// lines. Merging pre-counted refs (outcome aggregation) sums the counts.
func (o *RebindOutcome) AddUnresolved(ref UnresolvedRef) {
	if ref.Count == 0 {
		ref.Count = 1
	}
	for i := range o.Unresolved {
		u := &o.Unresolved[i]
		if u.GUID == ref.GUID && u.ItemType == ref.ItemType && u.Location == ref.Location {
			u.Count += ref.Count
			return
		}
	}
	o.Unresolved = append(o.Unresolved, ref)
}

// LocationReportBinding is the UnresolvedRef.Location for a report's
// definition.pbir dataset binding. Exported because the cmd layer uses it to
// route ownership: binding refs are collected by the dedicated report-binding
// pass, everything else by the per-item substitution pass.
const LocationReportBinding = "report dataset binding"

// reasonForStatus maps a target LookupStatus to the UnresolvedRef Reason.
func reasonForStatus(st LookupStatus) string {
	if st == LookupAmbiguous {
		return ReasonAmbiguous
	}
	return ReasonNotInTarget
}

// RebindChange records one applied baseline→target rewrite, for the deploy
// summary. Kind is "Lakehouse", "Workspace", or "SQL endpoint". Name is the
// resolved item/workspace display name, shown alongside the GUIDs so the user
// can tell which reference is which.
type RebindChange struct {
	Kind string
	Name string
	Old  string
	New  string
}

// RebindOutcome bundles what a rebind pass produced: the applied changes (for
// the summary, deduped by Old within a pass) and the references it could not
// resolve (surfaced to the user).
type RebindOutcome struct {
	Changes        []RebindChange
	Unresolved     []UnresolvedRef
	ReportBindings []ReportBinding
}

// ReportBinding is a resolved report→semantic-model binding, surfaced in the
// compare step so the user sees which model a report will bind to (and in which
// target workspace) before deploying.
type ReportBinding struct {
	Report    string // report display name
	Model     string // resolved semantic-model name
	Workspace string // target workspace the model lives in
}

// Substitution is a futils-native find→replace rule, the engine-side mirror of
// config.Substitution (kept config-free). Replacement is a literal (Literal) or
// the resolved attribute of a target item looked up by name in the target env.
// For IsRegex rules, compiled holds the pre-compiled *regexp.Regexp set by
// SetSubstitutions so the pattern is compiled once instead of once per part.
type Substitution struct {
	FindValue  string
	IsRegex    bool
	ItemType   string
	ItemName   string
	FilePath   string
	TargetType string
	TargetName string
	Attr       string
	Literal    string
	compiled   *regexp.Regexp // non-nil for valid IsRegex rules after SetSubstitutions
}

// Rebinder translates baseline-environment GUIDs to a target env by item name.
//
// The lazy SQL-endpoint cache (targetEndpoint) is guarded by mu so the
// rebinder is safe to share across the concurrent per-item compare workers.
// Every other field is set in NewRebinder / SetSubstitutions and never mutated
// afterwards (the *NameIndex maps are build-once, read-only), so only the
// endpoint cache needs the lock. substitutions is installed once before any
// concurrent use, so it is read-only during the compare.
type Rebinder struct {
	client    FabricClient
	token     string
	baseline  *NameIndex
	target    *NameIndex
	overrides map[string]Override // baseline GUID -> override

	mu             sync.Mutex           // guards targetEndpoint
	targetEndpoint map[string][2]string // target lakehouse GUID -> {host, id} (cache, guarded by mu)
	targetWSNames  map[string]string    // target workspace GUID -> display name (for summaries)

	substitutions []Substitution
}

// workspaceName returns the display name of a target workspace GUID, or the
// GUID itself when unknown — so a rebind summary always shows something useful.
func (rb *Rebinder) workspaceName(guid string) string {
	if n, ok := rb.targetWSNames[guid]; ok {
		return n
	}
	return guid
}

// NewRebinder builds the baseline and target name indices and returns a
// Rebinder. baselineWS / targetWS are each env's full workspace set (deploy
// targets plus reference-only workspaces). A nil overrides map is treated as
// empty.
func NewRebinder(client FabricClient, token string, baselineWS, targetWS []fabric.Workspace, overrides map[string]Override) (*Rebinder, error) {
	b, err := BuildNameIndex(client, token, baselineWS)
	if err != nil {
		return nil, err
	}
	t, err := BuildNameIndex(client, token, targetWS)
	if err != nil {
		return nil, err
	}
	if overrides == nil {
		overrides = map[string]Override{}
	}
	wsNames := make(map[string]string, len(targetWS))
	for _, w := range targetWS {
		wsNames[w.ID] = w.DisplayName
	}
	return &Rebinder{client: client, token: token, baseline: b, target: t, overrides: overrides, targetWSNames: wsNames}, nil
}

// resolveGUIDReason translates one baseline GUID to its target item, returning a
// Reason when it can't. Override (highest precedence) resolves its name in the
// target using ov.ItemType exactly, regardless of targetType; otherwise the
// baseline index supplies the name and the target index supplies the GUID.
// targetType, when non-empty, overrides base.Type for the forward lookup —
// needed by the SQL pass, whose baked GUID is often indexed as a SQLEndpoint
// item but must resolve to the same-named target LAKEHOUSE (targetEndpointFor
// needs the lakehouse GUID, not a SQLEndpoint item). Pass "" to use base.Type
// as before.
func (rb *Rebinder) resolveGUIDReason(guid, targetType string) (IndexedItem, bool, string) {
	if ov, ok := rb.overrides[guid]; ok {
		it, st := rb.target.LookupName(ov.ItemName, ov.ItemType)
		return it, st == LookupFound, reasonForStatus(st)
	}
	base, ok := rb.baseline.ItemByGUID(guid)
	if !ok {
		return IndexedItem{}, false, ReasonNameUnknown
	}
	typ := base.Type
	if targetType != "" {
		typ = targetType
	}
	it, st := rb.target.LookupName(base.Name, typ)
	return it, st == LookupFound, reasonForStatus(st)
}

// resolveNameReason resolves a NAME-form reference — the baseline index plays
// no part, because the reference already carries the item name (e.g.
// Sql.Database("host", "LH_Gold")). An override keyed by the literal name takes
// precedence, mirroring resolveGUIDReason's precedence for GUIDs; otherwise the
// name resolves directly in the target index.
func (rb *Rebinder) resolveNameReason(name, targetType string) (IndexedItem, bool, string) {
	if ov, ok := rb.overrides[name]; ok {
		it, st := rb.target.LookupName(ov.ItemName, ov.ItemType)
		return it, st == LookupFound, reasonForStatus(st)
	}
	it, st := rb.target.LookupName(name, targetType)
	return it, st == LookupFound, reasonForStatus(st)
}

// addChange records a baseline→target rewrite on out, deduplicated by Old via
// seen. No-ops on an empty, identity, or already-seen Old value. Shared by every
// rebind pass so the dedup rule lives in one place.
func addChange(out *RebindOutcome, seen map[string]bool, kind, name, oldV, newV string) {
	if oldV == "" || oldV == newV || seen[oldV] {
		return
	}
	seen[oldV] = true
	out.Changes = append(out.Changes, RebindChange{Kind: kind, Name: name, Old: oldV, New: newV})
}

// recordChangePair appends a RebindChange to out, deduplicated by the (old,new)
// PAIR via seen — unlike addChange, which dedups by old alone. Needed wherever a
// single old value can legitimately resolve to two different new values within
// one pass (e.g. two SQL/OneLake sources sharing a baseline host or workspace
// GUID that resolve to different targets): deduping by old alone would silently
// drop the second, distinct rewrite from the display.
func recordChangePair(out *RebindOutcome, seen map[string]bool, kind, name, oldV, newV string) {
	if oldV == "" || oldV == newV {
		return
	}
	key := oldV + "\x00" + newV
	if seen[key] {
		return
	}
	seen[key] = true
	out.Changes = append(out.Changes, RebindChange{Kind: kind, Name: name, Old: oldV, New: newV})
}

// applyChanges string-replaces every recorded rewrite in s. Replacing a value no
// longer present is a harmless no-op, so an accumulated set (e.g. across the
// semantic-model passes) can be applied safely.
func applyChanges(s string, changes []RebindChange) string {
	for _, c := range changes {
		s = strings.ReplaceAll(s, c.Old, c.New)
	}
	return s
}

// RebindPart dispatches a single item part to the right rebind pass by item
// type and part name, returning the rewritten bytes and the outcome. Parts with
// no recognized reference location are returned unchanged.
func (rb *Rebinder) RebindPart(item LocalItem, partPath string, content []byte) ([]byte, RebindOutcome) {
	if strings.HasPrefix(path.Base(partPath), "notebook-content.") {
		return rb.RebindNotebookLakehouses(content)
	}
	if item.Type == "SemanticModel" {
		return rb.RebindSemanticModel(content)
	}
	if item.Type == "Report" && path.Base(partPath) == "definition.pbir" {
		return rb.RebindReportConnection(item, content)
	}
	if item.Type == "Lakehouse" && path.Base(partPath) == "shortcuts.metadata.json" {
		return rb.RebindShortcuts(content)
	}
	return content, RebindOutcome{}
}

// RebindShortcuts rewrites the OneLake target GUIDs in a lakehouse's
// shortcuts.metadata.json from baseline to target, by name. shortcuts.metadata.json
// is a Lakehouse definition part, so it already deploys with the item — but the
// items API stores the targets verbatim (unlike Fabric's own deployment
// pipelines, it does NOT auto-remap internal shortcuts), so a shortcut pointing
// at a baseline lakehouse would keep pointing there after deploy. For each
// OneLake target we resolve itemId (a baseline GUID) to the same-named item in
// the target env and rewrite both itemId and its workspaceId. Self-references
// (empty/zero GUIDs, which Fabric maps to the current lakehouse), external
// targets (ADLS/S3/etc.), and unparseable content are left untouched; an
// unresolvable OneLake target is surfaced as UnresolvedRef and left as-is.
func (rb *Rebinder) RebindShortcuts(content []byte) ([]byte, RebindOutcome) {
	var shortcuts []struct {
		Name   string `json:"name"`
		Target struct {
			OneLake *struct {
				WorkspaceID string `json:"workspaceId"`
				ItemID      string `json:"itemId"`
			} `json:"oneLake"`
		} `json:"target"`
	}
	if err := json.Unmarshal(content, &shortcuts); err != nil {
		return content, RebindOutcome{} // not the array shape we rewrite — leave it
	}
	var out RebindOutcome
	seen := map[string]bool{}
	for _, sc := range shortcuts {
		ol := sc.Target.OneLake
		if ol == nil || isZeroOrEmptyGUID(ol.ItemID) {
			continue // external target, or self-reference Fabric maps itself
		}
		it, ok, reason := rb.resolveGUIDReason(ol.ItemID, "")
		if !ok {
			out.AddUnresolved(UnresolvedRef{GUID: ol.ItemID, ItemType: "Lakehouse", Location: "shortcut target", Reason: reason})
			continue
		}
		addChange(&out, seen, "Shortcut", it.Name, ol.ItemID, it.GUID)
		if ol.WorkspaceID != "" && it.WorkspaceID != "" {
			addChange(&out, seen, "Workspace", rb.workspaceName(it.WorkspaceID), ol.WorkspaceID, it.WorkspaceID)
		}
	}
	return []byte(applyChanges(string(content), out.Changes)), out
}

// isZeroOrEmptyGUID reports whether a shortcut GUID is a self-reference: empty,
// or the all-zeros GUID Fabric maps to the current lakehouse/workspace.
func isZeroOrEmptyGUID(guid string) bool {
	return guid == "" || guid == "00000000-0000-0000-0000-000000000000"
}

// RebindNotebookLakehouses rewrites the lakehouse dependency GUIDs in a Fabric
// notebook part from baseline to target, by name. It only touches GUIDs found
// in the dependencies.lakehouse metadata block (never arbitrary UUIDs in code),
// records each applied rewrite as a RebindChange, and string-replaces those
// exact GUIDs throughout the content. GUIDs it cannot resolve become
// UnresolvedRef and are left unchanged. Content with no lakehouse block is
// returned unchanged with an empty outcome.
func (rb *Rebinder) RebindNotebookLakehouses(content []byte) ([]byte, RebindOutcome) {
	lh, ok := parseNotebookLakehouse(content)
	if !ok {
		return content, RebindOutcome{}
	}
	var out RebindOutcome
	seen := map[string]bool{}

	if lh.DefaultLakehouse != "" {
		var it IndexedItem
		var resolved bool
		var reason string
		if _, hasOverride := rb.overrides[lh.DefaultLakehouse]; !hasOverride && lh.DefaultLakehouseName != "" {
			var st LookupStatus
			it, st = rb.target.LookupName(lh.DefaultLakehouseName, "Lakehouse")
			resolved = st == LookupFound
			reason = reasonForStatus(st)
		} else {
			it, resolved, reason = rb.resolveGUIDReason(lh.DefaultLakehouse, "")
		}
		if resolved {
			addChange(&out, seen, "Lakehouse", it.Name, lh.DefaultLakehouse, it.GUID)
			if lh.DefaultLakehouseWorkspaceID != "" && it.WorkspaceID != "" {
				addChange(&out, seen, "Workspace", rb.workspaceName(it.WorkspaceID), lh.DefaultLakehouseWorkspaceID, it.WorkspaceID)
			}
		} else {
			out.AddUnresolved(UnresolvedRef{GUID: lh.DefaultLakehouse, ItemType: "Lakehouse", Location: "default_lakehouse", Reason: reason})
		}
	}
	for _, k := range lh.KnownLakehouses {
		if k.ID == "" || seen[k.ID] {
			continue
		}
		if it, ok, reason := rb.resolveGUIDReason(k.ID, ""); ok {
			addChange(&out, seen, "Lakehouse", it.Name, k.ID, it.GUID)
		} else {
			out.AddUnresolved(UnresolvedRef{GUID: k.ID, ItemType: "Lakehouse", Location: "known_lakehouses", Reason: reason})
		}
	}

	return []byte(applyChanges(string(content), out.Changes)), out
}

// SetSubstitutions installs the customer's custom find→replace rules. Called by
// the cmd layer after NewRebinder (config→engine conversion lives there). Each
// IsRegex rule has its pattern compiled once here; a rule whose pattern is
// invalid is stored with a nil compiled field and silently skipped at apply time.
func (rb *Rebinder) SetSubstitutions(subs []Substitution) {
	for i := range subs {
		if subs[i].IsRegex && subs[i].FindValue != "" {
			re, err := regexp.Compile(subs[i].FindValue)
			if err == nil {
				subs[i].compiled = re
			}
			// Invalid pattern: compiled stays nil; rule is skipped in ApplyCustomSubstitutions.
		}
	}
	rb.substitutions = subs
}

// ApplyCustomSubstitutions runs the customer's find→replace rules over one part.
// Each rule whose optional item/file filters match is applied: the replacement
// is a literal (Literal) or the resolved target attribute (by name in the
// target env). Applied rewrites are recorded as RebindChange{Kind:"Substitution"};
// rules whose target can't be resolved are left unapplied and surfaced as
// UnresolvedRef. Runs in the explicit tier (before auto-rebind).
func (rb *Rebinder) ApplyCustomSubstitutions(item LocalItem, partPath string, content []byte) ([]byte, RebindOutcome) {
	var out RebindOutcome
	s := string(content)
	for _, sub := range rb.substitutions {
		if sub.FindValue == "" {
			continue
		}
		if sub.ItemType != "" && sub.ItemType != item.Type {
			continue
		}
		if sub.ItemName != "" && sub.ItemName != item.DisplayName {
			continue
		}
		if sub.FilePath != "" {
			if ok, _ := path.Match(sub.FilePath, partPath); !ok {
				continue
			}
		}
		var repl string
		if sub.TargetType != "" {
			r, ok := rb.ResolveTargetAttr(sub.TargetType, sub.TargetName, sub.Attr)
			if !ok {
				out.AddUnresolved(UnresolvedRef{
					GUID: sub.FindValue, ItemType: sub.TargetType, Location: "custom substitution", Reason: ReasonNotInTarget,
				})
				continue
			}
			repl = r
		} else {
			repl = sub.Literal
		}
		if sub.IsRegex {
			re := sub.compiled
			if re == nil {
				continue // pattern failed to compile at SetSubstitutions time; skip
			}
			next := re.ReplaceAllString(s, repl)
			if next != s {
				// Record one RebindChange per distinct concrete matched value so
				// the summary shows what was actually replaced, not the raw pattern.
				// New is the EXPANDED replacement for that specific match (not the
				// raw template), so capture-group references like $1 resolve to the
				// concrete text that was written.
				seen := map[string]bool{}
				for _, m := range re.FindAllString(s, -1) {
					if !seen[m] {
						seen[m] = true
						expanded := re.ReplaceAllString(m, repl)
						out.Changes = append(out.Changes, RebindChange{Kind: "Substitution", Old: m, New: expanded})
					}
				}
				s = next
			}
		} else {
			next := strings.ReplaceAll(s, sub.FindValue, repl)
			if next != s {
				out.Changes = append(out.Changes, RebindChange{Kind: "Substitution", Old: sub.FindValue, New: repl})
				s = next
			}
		}
	}
	return []byte(s), out
}

// ResolveTargetAttr resolves a target item by name in the target env and returns
// the requested attribute: "id"/"" → the item GUID; "sqlendpoint"/"sqlendpointid"
// → the lakehouse's SQL endpoint host/id (via the cached endpoint lookup).
func (rb *Rebinder) ResolveTargetAttr(itemType, itemName, attr string) (string, bool) {
	it, ok := rb.target.ItemByName(itemName, itemType)
	if !ok {
		return "", false
	}
	switch attr {
	case "", "id":
		return it.GUID, true
	case "sqlendpoint":
		host, _, ok := rb.targetEndpointFor(it)
		return host, ok
	case "sqlendpointid":
		_, id, ok := rb.targetEndpointFor(it)
		return id, ok
	}
	return "", false
}

// reportConnGUID matches a canonical GUID inside a pbir connection string value.
var reportConnGUID = regexp.MustCompile("^" + guidPat + "$")

// parseConnString pulls case-insensitive key=value pairs out of a pbir
// byConnection connectionString (semicolon-delimited, values may be quoted).
func parseConnString(cs string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(cs, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		out[k] = v
	}
	return out
}

// parseFlatConn extracts the semantic-model identifiers from a flat byConnection
// connectionString. modelName is the "initial catalog" display name, or "" when
// it is absent or holds a GUID (a GUID is never a usable model name). modelGUID
// is "semanticmodelid", or the catalog GUID when that is the only GUID present
// (catalog-GUID promotion). An empty connectionString yields ("", "").
func parseFlatConn(cs string) (modelName, modelGUID string) {
	kv := parseConnString(cs)
	modelGUID = kv["semanticmodelid"]
	modelName = kv["initial catalog"]
	if reportConnGUID.MatchString(modelName) {
		if modelGUID == "" {
			modelGUID = modelName
		}
		modelName = ""
	}
	return modelName, modelGUID
}

// canonicalByConnection is the fabric-cicd canonical byConnection block. Field
// ORDER is significant: a struct (not map[string]any) serializes in declaration
// order, matching the on-disk fabric-cicd form, where map keys would sort
// alphabetically. ConnectionString and PbiServiceModelID are *string left nil
// (no omitempty) so they emit JSON null rather than being dropped.
type canonicalByConnection struct {
	ByConnection canonicalByConnectionInner `json:"byConnection"`
}

type canonicalByConnectionInner struct {
	ConnectionString          *string `json:"connectionString"`
	PbiServiceModelID         *string `json:"pbiServiceModelId"`
	PbiModelVirtualServerName string  `json:"pbiModelVirtualServerName"`
	PbiModelDatabaseName      string  `json:"pbiModelDatabaseName"`
	Name                      string  `json:"name"`
	ConnectionType            string  `json:"connectionType"`
}

// RebindReportConnection rewrites a report's definition.pbir byConnection
// reference from baseline to target by NAME. It handles both on-disk shapes:
// the Power BI Desktop flat connectionString (Data Source + initial catalog +
// semanticmodelid) and the fabric-cicd structured form (pbiModelDatabaseName).
// On success it replaces datasetReference with the canonical byConnection form
// (connectionString null, pbiModelDatabaseName = target model GUID), so the
// published payload binds the report to the target model in a single publish.
// byPath and reference-less reports are returned unchanged.
// resolveModelName maps a report's byConnection reference to its model display
// name using the authoritative precedence: an override for the baseline GUID
// wins, else the baseline-index name for that GUID, else the parsed
// connectionString name candidate (used only when the GUID is unknown to the
// baseline index — e.g. a flat string whose model isn't in the baseline). The
// baseline GUID is authoritative so a stale 'initial catalog' string can't
// misbind. Returns "" when nothing resolves. Shared by RebindReportConnection
// (in-payload rewrite) and reportDatasetRef (post-deploy co-deploy match) so a
// single resolution rule governs both.
func (rb *Rebinder) resolveModelName(nameCandidate, baselineGUID string) string {
	modelName := nameCandidate
	if base, found := rb.baseline.ItemByGUID(baselineGUID); found {
		modelName = base.Name
	}
	if ov, ok := rb.overrides[baselineGUID]; ok {
		modelName = ov.ItemName
	}
	return modelName
}

func (rb *Rebinder) RebindReportConnection(item LocalItem, content []byte) ([]byte, RebindOutcome) {
	var out RebindOutcome
	var pbir map[string]json.RawMessage
	if err := json.Unmarshal(content, &pbir); err != nil {
		return content, out
	}
	dsRaw, ok := pbir["datasetReference"]
	if !ok {
		return content, out
	}
	var ds struct {
		ByConnection *struct {
			ConnectionString     *string `json:"connectionString"`
			PbiModelDatabaseName string  `json:"pbiModelDatabaseName"`
		} `json:"byConnection"`
	}
	if err := json.Unmarshal(dsRaw, &ds); err != nil || ds.ByConnection == nil {
		return content, out // byPath / reference-less / unparseable
	}

	// Extract the baseline model GUID and a name candidate from whichever
	// byConnection shape is present.
	var nameCandidate, baselineGUID string
	if cs := ds.ByConnection.ConnectionString; cs != nil && *cs != "" {
		nameCandidate, baselineGUID = parseFlatConn(*cs)
	} else {
		baselineGUID = ds.ByConnection.PbiModelDatabaseName
	}

	modelName := rb.resolveModelName(nameCandidate, baselineGUID)
	if modelName == "" {
		out.AddUnresolved(UnresolvedRef{
			GUID: baselineGUID, ItemType: "SemanticModel", Location: LocationReportBinding, Reason: ReasonNameUnknown,
		})
		return content, out
	}

	it, st := rb.target.LookupName(modelName, "SemanticModel")
	if st != LookupFound {
		out.AddUnresolved(UnresolvedRef{
			GUID: baselineGUID, ItemType: "SemanticModel", Location: LocationReportBinding, Reason: reasonForStatus(st),
		})
		return content, out
	}

	canonical, err := json.Marshal(canonicalByConnection{
		ByConnection: canonicalByConnectionInner{
			PbiModelVirtualServerName: "sobe_wowvirtualserver",
			PbiModelDatabaseName:      it.GUID,
			Name:                      "EntityDataSource",
			ConnectionType:            "pbiServiceXmlaStyleLive",
		},
	})
	if err != nil {
		return content, out
	}
	pbir["$schema"] = json.RawMessage(`"https://developer.microsoft.com/json-schemas/fabric/item/report/definitionProperties/1.0.0/schema.json"`)
	pbir["datasetReference"] = canonical
	rewritten, err := json.MarshalIndent(pbir, "", "  ")
	if err != nil {
		return content, out
	}

	out.ReportBindings = append(out.ReportBindings, ReportBinding{
		Report: item.DisplayName, Model: modelName, Workspace: rb.workspaceName(it.WorkspaceID),
	})
	return rewritten, out
}
