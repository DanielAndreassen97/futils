package deploy

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestBuildPlanActionsAndOrder(t *testing.T) {
	selected := []LocalItem{
		{Type: "Report", DisplayName: "R"},
		{Type: "SemanticModel", DisplayName: "M"},
	}
	deployed := []fabric.Item{
		{ID: "m-id", Type: "SemanticModel", DisplayName: "M"}, // exists -> Update
	}
	plan := BuildPlan(selected, deployed)

	if len(plan) != 2 {
		t.Fatalf("want 2 planned, got %d", len(plan))
	}
	// Ordered: SemanticModel before Report.
	if plan[0].Item.Type != "SemanticModel" || plan[1].Item.Type != "Report" {
		t.Fatalf("order wrong: %s then %s", plan[0].Item.Type, plan[1].Item.Type)
	}
	if plan[0].Action != ActionUpdate || plan[0].ExistingID != "m-id" {
		t.Errorf("model should update existing: %+v", plan[0])
	}
	if plan[1].Action != ActionCreate {
		t.Errorf("report should create: %+v", plan[1])
	}
}
