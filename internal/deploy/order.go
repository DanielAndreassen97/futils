package deploy

import "sort"

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
// items of the same type keep their discovery order.
func SortForPublish(items []LocalItem) []LocalItem {
	out := make([]LocalItem, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		return orderOf(out[i].Type) < orderOf(out[j].Type)
	})
	return out
}
