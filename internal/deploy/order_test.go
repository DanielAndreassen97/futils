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
