package cmd

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

func TestRenderSchemaCompareTerminal(t *testing.T) {
	diffs := []schemacompare.LakehouseDiff{{
		Lakehouse: "LH_Silver",
		Schemas:   []string{"Dim"},
		Matching:  41,
		Tables: []schemacompare.TableDiff{
			{Schema: "Dim", Table: "NewOne", Kind: schemacompare.TableNew},
			{Schema: "Dim", Table: "Ansatt", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
				{Name: "Epost", Kind: schemacompare.ColAdded, NewType: "string"},
				{Name: "Alder", Kind: schemacompare.ColTypeChanged, OldType: "int", NewType: "bigint"},
			}},
		},
	}}
	out := renderSchemaCompareTerminal("DEV", "TEST", diffs)

	for _, want := range []string{
		"LH_Silver", "DEV", "TEST",
		"+ NEW TABLE", "Dim.NewOne",
		"Dim.Ansatt", "+ Epost", "string",
		"~ Alder", "int → bigint",
		"= 41 matching",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q in:\n%s", want, out)
		}
	}
}
