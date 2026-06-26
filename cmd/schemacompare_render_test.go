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
		"Schema compare", "LH_Silver", "DEV", "TEST",
		"Dim.NewOne", "new table",
		"Dim.Ansatt", "Epost", "string",
		"Alder", "int → bigint",
		"41 unchanged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSchemaCompareTerminalIdentical(t *testing.T) {
	// A lakehouse with no differing tables shows a clear "no differences" line.
	diffs := []schemacompare.LakehouseDiff{{
		Lakehouse: "LH_Gold", Schemas: []string{"dbo"}, Matching: 12,
	}}
	out := renderSchemaCompareTerminal("DEV", "TEST", diffs)
	if !strings.Contains(out, "LH_Gold") || !strings.Contains(out, "no differences") {
		t.Errorf("expected a 'no differences' line for an identical lakehouse, got:\n%s", out)
	}
}
