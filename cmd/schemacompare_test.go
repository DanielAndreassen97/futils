package cmd

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestIntersectLakehousesByName(t *testing.T) {
	src := []fabric.Item{
		{DisplayName: "LH_Silver", Type: "Lakehouse"},
		{DisplayName: "LH_Gold", Type: "Lakehouse"},
		{DisplayName: "LH_DevOnly", Type: "Lakehouse"},
	}
	tgt := []fabric.Item{
		{DisplayName: "LH_Gold", Type: "Lakehouse"},
		{DisplayName: "LH_Silver", Type: "Lakehouse"},
		{DisplayName: "LH_TestOnly", Type: "Lakehouse"},
	}
	got := intersectLakehousesByName(src, tgt)
	// Sorted, only names present on both sides.
	if len(got) != 2 || got[0] != "LH_Gold" || got[1] != "LH_Silver" {
		t.Errorf("intersect = %v, want [LH_Gold LH_Silver]", got)
	}
}
