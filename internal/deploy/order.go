package deploy

import (
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

	// dependsOn[i] = set of segment indices i's content references.
	indegree := make([]int, len(seg))
	dependents := make([][]int, len(seg)) // j -> list of i that depend on j
	for i, p := range seg {
		for lid, j := range byLogicalID {
			if j == i {
				continue
			}
			for _, part := range p.Parts {
				if strings.Contains(string(part.Content), lid) {
					dependents[j] = append(dependents[j], i)
					indegree[i]++
					break
				}
			}
		}
	}

	ordered := make([]LocalItem, 0, len(seg))
	placed := make([]bool, len(seg))
	for len(ordered) < len(seg) {
		advanced := false
		for i := range seg {
			if placed[i] || indegree[i] > 0 {
				continue
			}
			placed[i] = true
			ordered = append(ordered, seg[i])
			for _, dep := range dependents[i] {
				indegree[dep]--
			}
			advanced = true
		}
		if !advanced {
			// Cycle: append the remainder in existing order and stop.
			for i := range seg {
				if !placed[i] {
					ordered = append(ordered, seg[i])
				}
			}
			break
		}
	}
	copy(seg, ordered)
}
