package deploy

import "testing"

func TestSortForPublishModelBeforeReport(t *testing.T) {
	items := []LocalItem{
		{Type: "Report", DisplayName: "R"},
		{Type: "SemanticModel", DisplayName: "M"},
		{Type: "Notebook", DisplayName: "N"},
	}
	sorted := SortForPublish(items)
	pos := map[string]int{}
	for i, it := range sorted {
		pos[it.Type] = i
	}
	if pos["SemanticModel"] >= pos["Report"] {
		t.Errorf("SemanticModel must precede Report: %v", pos)
	}
}

func TestSortForPublishUnknownTypeLast(t *testing.T) {
	items := []LocalItem{
		{Type: "Frobnicator", DisplayName: "F"},
		{Type: "Notebook", DisplayName: "N"},
	}
	sorted := SortForPublish(items)
	if sorted[0].Type != "Notebook" || sorted[1].Type != "Frobnicator" {
		t.Errorf("unknown type should sort last: %+v", sorted)
	}
}

// Pipelines that invoke other repo pipelines (referenced by logicalId in
// pipeline-content.json) must publish AFTER their dependency, so a create of
// the invoker resolves the invokee's fresh GUID via the logicalId map.
func TestSortForPublishPipelineDependencies(t *testing.T) {
	const (
		lidA = "aaaaaaaa-0000-0000-0000-000000000001"
		lidB = "bbbbbbbb-0000-0000-0000-000000000002"
		lidC = "cccccccc-0000-0000-0000-000000000003"
	)
	pl := func(name, lid, content string) LocalItem {
		return LocalItem{Type: "DataPipeline", DisplayName: name, LogicalID: lid,
			Parts: []Part{{Path: "pipeline-content.json", Content: []byte(content)}}}
	}
	items := []LocalItem{
		// Input order is exactly wrong: master (invokes A and B) first.
		pl("PL_master", lidC, `{"activities":[{"type":"InvokePipeline","pipelineId":"`+lidA+`"},{"pipelineId":"`+lidB+`"}]}`),
		pl("PL_a", lidA, `{"activities":[{"type":"Copy"}]}`),
		{Type: "Notebook", DisplayName: "NB", Parts: []Part{{Path: "notebook-content.py", Content: []byte("x=1")}}},
		pl("PL_b", lidB, `{"activities":[{"type":"InvokePipeline","pipelineId":"`+lidA+`"}]}`),
	}
	sorted := SortForPublish(items)
	pos := map[string]int{}
	for i, it := range sorted {
		pos[it.DisplayName] = i
	}
	if !(pos["PL_a"] < pos["PL_b"] && pos["PL_b"] < pos["PL_master"]) {
		t.Errorf("dependency order wrong: %v", pos)
	}
	// The notebook still publishes before every pipeline (type order).
	if pos["NB"] > pos["PL_a"] {
		t.Errorf("type ordering must be untouched: %v", pos)
	}
}

// A dependency cycle (not constructible in Fabric's designer, but git can
// hold anything) degrades to the existing stable order instead of hanging
// or dropping items.
func TestSortForPublishPipelineCycleFallsBack(t *testing.T) {
	const (
		lidA = "aaaaaaaa-0000-0000-0000-000000000001"
		lidB = "bbbbbbbb-0000-0000-0000-000000000002"
	)
	items := []LocalItem{
		{Type: "DataPipeline", DisplayName: "PL_x", LogicalID: lidA,
			Parts: []Part{{Path: "pipeline-content.json", Content: []byte(`{"ref":"` + lidB + `"}`)}}},
		{Type: "DataPipeline", DisplayName: "PL_y", LogicalID: lidB,
			Parts: []Part{{Path: "pipeline-content.json", Content: []byte(`{"ref":"` + lidA + `"}`)}}},
	}
	sorted := SortForPublish(items)
	if len(sorted) != 2 || sorted[0].DisplayName != "PL_x" || sorted[1].DisplayName != "PL_y" {
		t.Errorf("cycle must keep stable input order, got %+v", sorted)
	}
}

// A lakehouse whose shortcut points at another planned lakehouse must publish
// AFTER its target — shortcut targets are baseline GUIDs, resolved to names
// via the supplied resolver and matched against the planned display names.
func TestSortLakehouseShortcutDependencies(t *testing.T) {
	bronzeGUID := "bbbb2222-2222-2222-2222-222222222222"
	shortcut := []byte(`[{"name":"s","target":{"type":"OneLake","oneLake":{"workspaceId":"w","itemId":"` + bronzeGUID + `","path":"Tables/t"}}}]`)
	plan := []PlannedItem{
		{Action: ActionCreate, Item: LocalItem{Type: "Warehouse", DisplayName: "WH"}},
		{Action: ActionCreate, Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_A_Consumer",
			Parts: []Part{{Path: "shortcuts.metadata.json", Content: shortcut}}}},
		{Action: ActionCreate, Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_Z_Target"}},
		{Action: ActionCreate, Item: LocalItem{Type: "Notebook", DisplayName: "NB"}},
	}
	baselineName := func(guid string) (string, bool) {
		if guid == bronzeGUID {
			return "LH_Z_Target", true
		}
		return "", false
	}
	sortLakehouseShortcutDeps(plan, baselineName)
	var lakehouses []string
	for _, p := range plan {
		if p.Item.Type == "Lakehouse" {
			lakehouses = append(lakehouses, p.Item.DisplayName)
		}
	}
	if len(lakehouses) != 2 || lakehouses[0] != "LH_Z_Target" || lakehouses[1] != "LH_A_Consumer" {
		t.Errorf("shortcut target must publish before its consumer, got %v", lakehouses)
	}
	if plan[0].Item.DisplayName != "WH" || plan[3].Item.DisplayName != "NB" {
		t.Errorf("non-lakehouse neighbours must not move, got %+v", plan)
	}
}

// Mutual shortcuts (a cycle) must degrade to the existing order, not hang or drop items.
func TestSortLakehouseShortcutCycleFallsBack(t *testing.T) {
	guidA, guidB := "aaaa1111-0000-0000-0000-000000000001", "aaaa1111-0000-0000-0000-000000000002"
	sc := func(guid string) []Part {
		return []Part{{Path: "shortcuts.metadata.json",
			Content: []byte(`[{"name":"s","target":{"type":"OneLake","oneLake":{"workspaceId":"w","itemId":"` + guid + `","path":"p"}}}]`)}}
	}
	plan := []PlannedItem{
		{Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_A", Parts: sc(guidB)}},
		{Item: LocalItem{Type: "Lakehouse", DisplayName: "LH_B", Parts: sc(guidA)}},
	}
	baselineName := func(guid string) (string, bool) {
		switch guid {
		case guidA:
			return "LH_A", true
		case guidB:
			return "LH_B", true
		}
		return "", false
	}
	sortLakehouseShortcutDeps(plan, baselineName)
	if plan[0].Item.DisplayName != "LH_A" || plan[1].Item.DisplayName != "LH_B" {
		t.Errorf("cycle must keep stable input order, got %+v", plan)
	}
}
