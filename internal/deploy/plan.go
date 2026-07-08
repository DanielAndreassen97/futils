package deploy

import "github.com/DanielAndreassen97/futils/internal/fabric"

type Action int

const (
	ActionCreate Action = iota
	ActionUpdate
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionUpdate:
		return "Update"
	case ActionDelete:
		return "Delete"
	default:
		return "Create"
	}
}

// PlannedItem is one item to publish, with its resolved create/update action.
type PlannedItem struct {
	Item       LocalItem
	Action     Action
	ExistingID string // set when Action == ActionUpdate
}

// BuildPlan turns the user-selected local items into an ordered publish plan.
// An item already present in the target (matched by type+name) becomes an
// Update against its existing ID; otherwise a Create. The plan is sorted by
// publish priority so dependencies (e.g. semantic models) go first.
func BuildPlan(selected []LocalItem, deployed []fabric.Item) []PlannedItem {
	deployedByKey := make(map[string]fabric.Item, len(deployed))
	for _, d := range deployed {
		deployedByKey[key(d.Type, d.DisplayName)] = d
	}
	ordered := SortForPublish(selected)
	plan := make([]PlannedItem, 0, len(ordered))
	for _, it := range ordered {
		p := PlannedItem{Item: it, Action: ActionCreate}
		if d, ok := deployedByKey[key(it.Type, it.DisplayName)]; ok {
			p.Action = ActionUpdate
			p.ExistingID = d.ID
		}
		plan = append(plan, p)
	}
	return plan
}
