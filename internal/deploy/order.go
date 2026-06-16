package deploy

import "sort"

// publishOrder mirrors fabric-cicd's SERIAL_ITEM_PUBLISH_ORDER for the types
// Phase 1 publishes plus the dependency anchors around them. Lower = earlier.
// Phase 3 replaces this with the full 27-type table from constants.py; the
// only invariant Phase 1 relies on is SemanticModel < Report (rebind needs the
// model deployed first) and that data containers precede their consumers.
var publishOrder = map[string]int{
	"VariableLibrary": 1,
	"Lakehouse":       2,
	"Warehouse":       3,
	"Environment":     4,
	"Notebook":        10,
	"DataPipeline":    11,
	"SemanticModel":   20,
	"Report":          21,
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
