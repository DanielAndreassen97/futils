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
	// WorkspaceFolder is the target workspace folder path a newly-created item
	// should land in, derived from its repo path under the mapping root ("" =
	// workspace root). Only consulted for ActionCreate — existing items keep
	// their current placement.
	WorkspaceFolder string
}

// BuildPlan turns the user-selected local items into an ordered publish plan.
// An item already present in the target (matched by type+name) becomes an
// Update against its existing ID; otherwise a Create. The plan is sorted by
// publish priority so dependencies (e.g. semantic models) go first.
// mappingRoot is the repo folder this group deploys from; it's stripped when
// deriving each new item's target workspace folder (empty = whole-repo mapping).
func BuildPlan(selected []LocalItem, deployed []fabric.Item, mappingRoot string) []PlannedItem {
	deployedByKey := make(map[string]fabric.Item, len(deployed))
	for _, d := range deployed {
		deployedByKey[key(d.Type, d.DisplayName)] = d
	}
	ordered := SortForPublish(selected)
	plan := make([]PlannedItem, 0, len(ordered))
	for _, it := range ordered {
		p := PlannedItem{Item: it, Action: ActionCreate, WorkspaceFolder: WorkspaceFolderPath(it.FolderPath, mappingRoot)}
		if d, ok := deployedByKey[key(it.Type, it.DisplayName)]; ok {
			p.Action = ActionUpdate
			p.ExistingID = d.ID
		}
		plan = append(plan, p)
	}
	return plan
}
