package deploy

import (
	"encoding/json"
	"path"
	"sort"
	"strings"
)

// publishOrder mirrors fabric-cicd's full SERIAL_ITEM_PUBLISH_ORDER
// (constants.py). Lower = earlier. The invariants deploys rely on: variable
// libraries first (everything may reference them), data containers before
// their consumers, and SemanticModel before Report (rebind needs the model
// deployed first). Unknown/new types sort last and deploy fine — order only
// matters between items that reference each other.
var publishOrder = map[string]int{
	"VariableLibrary":    1,
	"Warehouse":          2,
	"MirroredDatabase":   3,
	"Lakehouse":          4,
	"SQLDatabase":        5,
	"Environment":        6,
	"UserDataFunction":   7,
	"Eventhouse":         8,
	"SparkJobDefinition": 9,
	"Notebook":           10,
	"SemanticModel":      11,
	"Report":             12,
	"PaginatedReport":    13,
	"CopyJob":            14,
	"DataBuildToolJob":   15,
	"KQLDatabase":        16,
	"KQLQueryset":        17,
	"Dataflow":           18,
	"DataPipeline":       19,
	"Reflex":             20,
	"Eventstream":        21,
	"KQLDashboard":       22,
	"GraphQLApi":         23,
	"ApacheAirflowJob":   24,
	"MountedDataFactory": 25,
	"DataAgent":          26,
	"MLExperiment":       27,
	"Ontology":           28,
	"Map":                29,
}

// orderOf returns an item type's publish priority; unknown types sort last
// (large constant) but keep their relative input order via the stable sort.
func orderOf(itemType string) int {
	if o, ok := publishOrder[itemType]; ok {
		return o
	}
	return 1000
}

// SortForPublish returns a new slice ordered by publish priority. Stable, so
// items of the same type keep their discovery order — except DataPipelines,
// which get a dependency-ordering pass within the type (see
// sortPipelinesByDependency).
func SortForPublish(items []LocalItem) []LocalItem {
	out := make([]LocalItem, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		return orderOf(out[i].Type) < orderOf(out[j].Type)
	})
	sortPipelinesByDependency(out)
	return out
}

// sortPipelinesByDependency reorders the (contiguous, post type-sort)
// DataPipeline run of items so a pipeline that invokes another repo pipeline
// publishes AFTER it. Fabric git-sync serializes an InvokePipeline reference
// to a sibling repo pipeline as that pipeline's logicalId (same convention
// fabric-cicd's dependency scan relies on), so the edge test is simply
// "does A's content contain B's logicalId". Kahn's algorithm, stable: ready
// nodes keep their existing relative order, so unrelated pipelines don't
// shuffle. A cycle (not constructible in Fabric's designer, but git can hold
// anything) degrades to the existing order for the cyclic remainder —
// publish then surfaces any real failure per item instead of futils guessing.
func sortPipelinesByDependency(items []LocalItem) {
	start, end := -1, -1
	for i, it := range items {
		if it.Type == "DataPipeline" {
			if start == -1 {
				start = i
			}
			end = i + 1
		}
	}
	if start == -1 || end-start < 2 {
		return
	}
	seg := items[start:end]

	byLogicalID := make(map[string]int, len(seg)) // logicalId -> segment index
	for i, p := range seg {
		if p.LogicalID != "" {
			byLogicalID[p.LogicalID] = i
		}
	}

	// dependsOn[i] = segment indices i's content references.
	dependsOn := make([][]int, len(seg))
	for i, p := range seg {
		for lid, j := range byLogicalID {
			if j == i {
				continue
			}
			for _, part := range p.Parts {
				if strings.Contains(string(part.Content), lid) {
					dependsOn[i] = append(dependsOn[i], j)
					break
				}
			}
		}
	}

	ordered := make([]LocalItem, len(seg))
	for k, idx := range stableTopoOrder(len(seg), dependsOn) {
		ordered[k] = seg[idx]
	}
	copy(seg, ordered)
}

// sortLakehouseShortcutDeps reorders the plan's (contiguous, post type-sort)
// Lakehouse run so a lakehouse whose shortcuts point at another planned
// lakehouse publishes AFTER its target. Unlike pipeline references, shortcut
// targets are BASELINE GUIDs (not logicalIds), so the edge test resolves each
// target GUID to its baseline name and matches it against the other planned
// lakehouses' display names. Combined with RegisterTargetItem, this lets
// RebindShortcuts resolve a target created in the same run — creating the
// dependent first would leave its shortcut pointing at the baseline
// environment. Stable Kahn; a cycle degrades to the existing order (two
// lakehouses shortcutting into each other still deploy — one shortcut then
// surfaces as unresolved, honestly).
func sortLakehouseShortcutDeps(plan []PlannedItem, baselineName func(guid string) (string, bool)) {
	start, end := -1, -1
	for i, p := range plan {
		if p.Item.Type == "Lakehouse" {
			if start == -1 {
				start = i
			}
			end = i + 1
		}
	}
	if start == -1 || end-start < 2 {
		return
	}
	seg := plan[start:end]

	byName := make(map[string]int, len(seg))
	for i, p := range seg {
		byName[p.Item.DisplayName] = i
	}
	dependsOn := make([][]int, len(seg))
	for i, p := range seg {
		for _, guid := range shortcutTargetGUIDs(p.Item) {
			name, ok := baselineName(guid)
			if !ok {
				continue
			}
			if j, ok := byName[name]; ok && j != i {
				dependsOn[i] = append(dependsOn[i], j)
			}
		}
	}

	ordered := make([]PlannedItem, len(seg))
	for k, idx := range stableTopoOrder(len(seg), dependsOn) {
		ordered[k] = seg[idx]
	}
	copy(seg, ordered)
}

// shortcutTargetGUIDs returns the non-self OneLake target itemIds in a
// lakehouse's shortcuts.metadata.json part; nil when the item has no such part
// or it isn't the array shape RebindShortcuts rewrites.
func shortcutTargetGUIDs(item LocalItem) []string {
	for _, part := range item.Parts {
		if path.Base(part.Path) != "shortcuts.metadata.json" {
			continue
		}
		var shortcuts []struct {
			Target struct {
				OneLake *struct {
					ItemID string `json:"itemId"`
				} `json:"oneLake"`
			} `json:"target"`
		}
		if json.Unmarshal(part.Content, &shortcuts) != nil {
			return nil
		}
		var ids []string
		for _, sc := range shortcuts {
			if ol := sc.Target.OneLake; ol != nil && !isZeroOrEmptyGUID(ol.ItemID) {
				ids = append(ids, ol.ItemID)
			}
		}
		return ids
	}
	return nil
}

// stableTopoOrder returns a Kahn's-algorithm ordering of n nodes where
// dependsOn[i] lists the nodes i must come after. Stable: ready nodes keep
// their existing relative order, so unrelated items never shuffle. Duplicate
// and self edges are ignored. A cycle degrades to the existing order for the
// cyclic remainder — publish then surfaces any real failure per item instead
// of the sorter guessing.
func stableTopoOrder(n int, dependsOn [][]int) []int {
	indegree := make([]int, n)
	dependents := make([][]int, n) // j -> list of i that depend on j
	for i, deps := range dependsOn {
		seen := map[int]bool{}
		for _, j := range deps {
			if j == i || seen[j] {
				continue
			}
			seen[j] = true
			dependents[j] = append(dependents[j], i)
			indegree[i]++
		}
	}

	order := make([]int, 0, n)
	placed := make([]bool, n)
	for len(order) < n {
		advanced := false
		for i := 0; i < n; i++ {
			if placed[i] || indegree[i] > 0 {
				continue
			}
			placed[i] = true
			order = append(order, i)
			for _, dep := range dependents[i] {
				indegree[dep]--
			}
			advanced = true
		}
		if !advanced {
			// Cycle: append the remainder in existing order and stop.
			for i := 0; i < n; i++ {
				if !placed[i] {
					order = append(order, i)
				}
			}
			break
		}
	}
	return order
}
