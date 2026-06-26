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
			{Schema: "Dim", Table: "Ansatt", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
				{Name: "<script>", Kind: schemacompare.ColAdded, NewType: "string"},
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
}
