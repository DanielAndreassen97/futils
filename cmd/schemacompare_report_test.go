// cmd/schemacompare_report_test.go
package cmd

import (
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

func TestRenderSchemaCompareReport(t *testing.T) {
	diffs := []schemacompare.LakehouseDiff{{
		Lakehouse: "LH_Silver",
		Schemas:   []string{"Dim"},
		Matching:  10,
		Tables: []schemacompare.TableDiff{
			{Schema: "Dim", Table: "NyTabell", Kind: schemacompare.TableNew},
			{Schema: "Dim", Table: "Ansatt", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
				{Name: "<script>", Kind: schemacompare.ColAdded, NewType: "string"},
				{Name: "Belop", Kind: schemacompare.ColTypeChanged, OldType: "int", NewType: "bigint"},
			}},
		},
	}}
	out := renderSchemaCompareReport("DEV", "TEST", diffs)

	if !strings.Contains(out, "<!doctype html>") {
		t.Error("expected a doctype")
	}
	if !strings.Contains(out, "LH_Silver") || !strings.Contains(out, "Dim.Ansatt") {
		t.Error("report missing lakehouse/table name")
	}
	// Content must be HTML-escaped.
	if strings.Contains(out, "<script>") {
		t.Error("column name not escaped — raw <script> present")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected escaped column name")
	}
	// Option B structure: changed tables render as a column grid, new tables as a
	// chipped row, and type changes show an explicit from → to (not a muted line).
	if !strings.Contains(out, `class="sc-cols"`) {
		t.Error("expected the structured column grid (sc-cols) for a changed table")
	}
	if !strings.Contains(out, "new table") {
		t.Error("expected a 'new table' chip for the added table")
	}
	if !strings.Contains(out, "Belop") || !strings.Contains(out, "int") || !strings.Contains(out, "bigint") {
		t.Error("expected the type change to render both old and new types")
	}
}

func TestRenderSchemaCompareReportIdentical(t *testing.T) {
	diffs := []schemacompare.LakehouseDiff{{
		Lakehouse: "LH_Gold", Schemas: []string{"dbo"}, Matching: 18,
	}}
	out := renderSchemaCompareReport("DEV", "TEST", diffs)
	if !strings.Contains(out, "LH_Gold") || !strings.Contains(out, "identical") {
		t.Errorf("expected an 'identical' marker for a lakehouse with no diffs, got:\n%s", out)
	}
}
