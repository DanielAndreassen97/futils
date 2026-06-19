package deploy

import (
	"path"
	"regexp"
	"strings"

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
}

const (
	ReasonNameUnknown = "name-unknown"  // baseline GUID not in baseline index — no name to match by
	ReasonNotInTarget = "not-in-target" // name known but absent from every registered target workspace
	ReasonAmbiguous   = "ambiguous"     // name appears in 2+ target workspaces
)

// reasonForStatus maps a target LookupStatus to the UnresolvedRef Reason.
func reasonForStatus(st LookupStatus) string {
	if st == LookupAmbiguous {
		return ReasonAmbiguous
	}
	return ReasonNotInTarget
}

// RebindChange records one applied baseline→target rewrite, for the deploy
// summary. Kind is "Lakehouse", "Workspace", or "SQL endpoint".
type RebindChange struct {
	Kind string
	Old  string
	New  string
}

// RebindOutcome bundles what a rebind pass produced: the applied changes (for
// the summary, deduped by Old within a pass) and the references it could not
// resolve (surfaced to the user).
type RebindOutcome struct {
	Changes    []RebindChange
	Unresolved []UnresolvedRef
}

// Substitution is a futils-native find→replace rule, the engine-side mirror of
// config.Substitution (kept config-free). Replacement is a literal (Literal) or
// the resolved attribute of a target item looked up by name in the target env.
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
}

// Rebinder translates baseline-environment GUIDs to a target env by item name.
type Rebinder struct {
	client    FabricClient
	token     string
	baseline  *NameIndex
	target    *NameIndex
	overrides map[string]Override // baseline GUID -> override

	baseEndpoints  map[string]IndexedItem // baseline SQL-endpoint id -> lakehouse (lazy)
	targetEndpoint map[string][2]string   // target lakehouse GUID -> {host, id} (cache)

	substitutions []Substitution
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
	return &Rebinder{client: client, token: token, baseline: b, target: t, overrides: overrides}, nil
}

// resolveGUIDReason translates one baseline GUID to its target item, returning a
// Reason when it can't. Override (highest precedence) resolves its name in the
// target; otherwise the baseline index supplies the name and the target index
// supplies the GUID.
func (rb *Rebinder) resolveGUIDReason(guid string) (IndexedItem, bool, string) {
	if ov, ok := rb.overrides[guid]; ok {
		it, st := rb.target.LookupName(ov.ItemName, ov.ItemType)
		return it, st == LookupFound, reasonForStatus(st)
	}
	base, ok := rb.baseline.ItemByGUID(guid)
	if !ok {
		return IndexedItem{}, false, ReasonNameUnknown
	}
	it, st := rb.target.LookupName(base.Name, base.Type)
	return it, st == LookupFound, reasonForStatus(st)
}

// resolveGUID translates one baseline GUID to its target item. An override
// (highest precedence) resolves its ItemName/ItemType directly in the target;
// otherwise the baseline index supplies the name and the target index supplies
// the new GUID. Returns false when it cannot be resolved.
func (rb *Rebinder) resolveGUID(guid string) (IndexedItem, bool) {
	it, ok, _ := rb.resolveGUIDReason(guid)
	return it, ok
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
	return content, RebindOutcome{}
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
	add := func(kind, oldG, newG string) {
		if oldG == "" || oldG == newG || seen[oldG] {
			return
		}
		seen[oldG] = true
		out.Changes = append(out.Changes, RebindChange{Kind: kind, Old: oldG, New: newG})
	}

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
			it, resolved, reason = rb.resolveGUIDReason(lh.DefaultLakehouse)
		}
		if resolved {
			add("Lakehouse", lh.DefaultLakehouse, it.GUID)
			if lh.DefaultLakehouseWorkspaceID != "" && it.WorkspaceID != "" {
				add("Workspace", lh.DefaultLakehouseWorkspaceID, it.WorkspaceID)
			}
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: lh.DefaultLakehouse, ItemType: "Lakehouse", Location: "default_lakehouse", Reason: reason})
		}
	}
	for _, k := range lh.KnownLakehouses {
		if k.ID == "" || seen[k.ID] {
			continue
		}
		if it, ok, reason := rb.resolveGUIDReason(k.ID); ok {
			add("Lakehouse", k.ID, it.GUID)
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: k.ID, ItemType: "Lakehouse", Location: "known_lakehouses", Reason: reason})
		}
	}

	s := string(content)
	for _, c := range out.Changes {
		s = strings.ReplaceAll(s, c.Old, c.New)
	}
	return []byte(s), out
}

// SetSubstitutions installs the customer's custom find→replace rules. Called by
// the cmd layer after NewRebinder (config→engine conversion lives there).
func (rb *Rebinder) SetSubstitutions(subs []Substitution) { rb.substitutions = subs }

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
				out.Unresolved = append(out.Unresolved, UnresolvedRef{
					GUID: sub.FindValue, ItemType: sub.TargetType, Location: "custom substitution", Reason: ReasonNotInTarget,
				})
				continue
			}
			repl = r
		} else {
			repl = sub.Literal
		}
		var next string
		if sub.IsRegex {
			re, err := regexp.Compile(sub.FindValue)
			if err != nil {
				continue // skip an invalid regex rather than abort the deploy
			}
			next = re.ReplaceAllString(s, repl)
		} else {
			next = strings.ReplaceAll(s, sub.FindValue, repl)
		}
		if next != s {
			out.Changes = append(out.Changes, RebindChange{Kind: "Substitution", Old: sub.FindValue, New: repl})
			s = next
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
