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
