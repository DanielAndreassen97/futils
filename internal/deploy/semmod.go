package deploy

import (
	"regexp"
	"strings"
)

// guidPat matches a canonical lowercase/upper GUID (used inside connection URLs).
const guidPat = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

// onelakeRe matches a Direct Lake on OneLake data-source URL, capturing the
// workspace GUID and the lakehouse/warehouse GUID:
// https://onelake.dfs.fabric.microsoft.com/<workspace>/<lakehouse>
// The workspace segment is captured as any non-slash sequence (production
// workspaces are GUIDs; test fixtures may use readable IDs).
var onelakeRe = regexp.MustCompile(`onelake\.dfs\.fabric\.microsoft\.com/([^/"]+)/(` + guidPat + `)`)

// rebindOneLakeSources rewrites every Direct Lake on OneLake source URL in s by
// resolving its lakehouse GUID baseline→target (by name, overrides honored) and
// its workspace GUID to the resolved lakehouse's target workspace. Applied
// changes and unresolved refs are appended to out. Returns the rewritten string.
func (rb *Rebinder) rebindOneLakeSources(s string, out *RebindOutcome) string {
	seen := map[string]bool{}
	add := func(kind, oldV, newV string) {
		if oldV == "" || oldV == newV || seen[oldV] {
			return
		}
		seen[oldV] = true
		out.Changes = append(out.Changes, RebindChange{Kind: kind, Old: oldV, New: newV})
	}
	for _, m := range onelakeRe.FindAllStringSubmatch(s, -1) {
		wsGUID, lhGUID := m[1], m[2]
		if it, ok := rb.resolveGUID(lhGUID); ok {
			add("Lakehouse", lhGUID, it.GUID)
			if it.WorkspaceID != "" {
				add("Workspace", wsGUID, it.WorkspaceID)
			}
		} else {
			out.Unresolved = append(out.Unresolved, UnresolvedRef{GUID: lhGUID, ItemType: "Lakehouse", Location: "onelake source"})
		}
	}
	for _, c := range out.Changes {
		s = strings.ReplaceAll(s, c.Old, c.New)
	}
	return s
}
