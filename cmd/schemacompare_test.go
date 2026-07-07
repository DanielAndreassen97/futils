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

// TestUnionSorted proves the schema pick-list covers BOTH sides: a schema that
// exists only in the destination must still be listed (source-only listing hid
// destination-only schemas and reported the lakehouse as identical).
func TestUnionSorted(t *testing.T) {
	got := unionSorted([]string{"dbo", "silver"}, []string{"gold", "dbo"})
	want := []string{"dbo", "gold", "silver"}
	if len(got) != len(want) {
		t.Fatalf("union = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("union = %v, want %v", got, want)
		}
	}
	if u := unionSorted(nil, []string{"a"}); len(u) != 1 || u[0] != "a" {
		t.Errorf("nil-side union = %v, want [a]", u)
	}
}
