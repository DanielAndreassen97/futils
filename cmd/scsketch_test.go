package cmd

import (
	"os"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// TestWriteSchemaCompareSketch is a throwaway generator for UI iteration: it
// renders the schema-compare report with a rich sample dataset and writes it
// where FUTILS_SC_SKETCH points. Skipped unless that env var is set — never
// runs in CI. Delete after the report design settles.
func TestWriteSchemaCompareSketch(t *testing.T) {
	out := os.Getenv("FUTILS_SC_SKETCH")
	if out == "" {
		t.Skip("set FUTILS_SC_SKETCH=<path> to write the sketch")
	}
	diffs := []schemacompare.LakehouseDiff{
		{
			Lakehouse: "LH_Gold", Schemas: []string{"Dim", "Fakta"}, Matching: 74,
			Tables: []schemacompare.TableDiff{
				{Schema: "Dim", Table: "DimAnsatt", Kind: schemacompare.TableNew},
				{Schema: "Dim", Table: "DimProsjekt", Kind: schemacompare.TableNew},
				{Schema: "Fakta", Table: "FaktaTimer", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
					{Name: "AntallTimer", Kind: schemacompare.ColTypeChanged, OldType: "int", NewType: "decimal(18,2)"},
					{Name: "ProsjektNokkel", Kind: schemacompare.ColAdded, NewType: "bigint"},
					{Name: "GammelKode", Kind: schemacompare.ColRemoved, OldType: "varchar(8000)"},
				}},
				{Schema: "Fakta", Table: "FaktaBudsjett", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
					{Name: "Belop", Kind: schemacompare.ColTypeChanged, OldType: "float", NewType: "decimal(18,2)"},
					{Name: "ValutaKode", Kind: schemacompare.ColAdded, NewType: "varchar(3)"},
				}},
				{Schema: "Dim", Table: "DimGammelOrg", Kind: schemacompare.TableRemoved},
			},
		},
		{Lakehouse: "LH_Silver", Schemas: []string{"dbo"}, Matching: 41},
		{
			Lakehouse: "LH_Bronze", Schemas: []string{"dbo"}, Matching: 18,
			Tables: []schemacompare.TableDiff{
				{Schema: "dbo", Table: "raw_sales_2026", Kind: schemacompare.TableNew},
			},
		},
	}
	html := renderSchemaCompareReport("DP - DEV - Data", "DP - TEST - Data", diffs)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
