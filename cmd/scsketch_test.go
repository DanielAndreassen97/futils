package cmd

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
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

// TestWriteDeployReportSketch renders the deploy report with a rich sample —
// multi-workspace results, reference rebinds, report bindings, post-deploy
// runs and a content diff — and writes it where FUTILS_DEPLOY_SKETCH points.
// Same throwaway design-iteration purpose as the schema-compare sketch.
func TestWriteDeployReportSketch(t *testing.T) {
	out := os.Getenv("FUTILS_DEPLOY_SKETCH")
	if out == "" {
		t.Skip("set FUTILS_DEPLOY_SKETCH=<path> to write the sketch")
	}
	wsData := fabric.Workspace{ID: "ws-data", DisplayName: "DP - TEST - Data"}
	wsSem := fabric.Workspace{ID: "ws-sem", DisplayName: "DP - TEST - SemMod"}
	groups := []deployGroup{
		{
			Folder: "Backend", Target: wsData,
			Changes: []deploy.RebindChange{
				{Kind: "Lakehouse", Name: "LH_Gold", Old: "1eb7fce4-1bd7-7916-37b8-d691f0cea556", New: "8244188e-2835-a6b1-ad48-8ba07a869431"},
				{Kind: "Workspace", Name: "DP - TEST - Data", Old: "2ca25a08-3655-3ce5-6b6c-eb643f3cd974", New: "327b42d0-4e4b-c1b2-8a49-2a0e48d903af"},
				{Kind: "SQL endpoint", Name: "LH_Gold", Old: "abcdef12-3456-7890-abcd-ef1234567890.datawarehouse.fabric.microsoft.com", New: "fedcba98-7654-3210-fedc-ba9876543210.datawarehouse.fabric.microsoft.com"},
				{Kind: "Shortcut", Name: "LH_Silver", Old: "954039b4-ccc9-836a-475d-8fa2b4ad5ecf", New: "92ea579c-04e4-bc1b-28a4-92a0e48d903a"},
			},
			Diffs: []ItemDiff{{
				Name: "nb_transform_sales", Type: "Notebook",
				Parts: []deploy.PartDiff{{
					Path: "notebook-content.py",
					Old:  "# Fabric notebook source\ndf = spark.read.table(\"sales\")\ndf.write.mode(\"overwrite\").saveAsTable(\"gold_sales\")",
					New:  "# Fabric notebook source\ndf = spark.read.table(\"sales\")\ndf = df.filter(df.amount > 0)\ndf.write.mode(\"overwrite\").saveAsTable(\"gold_sales\")",
				}},
			}},
		},
		{
			Folder: "Frontend", Target: wsSem,
			ReportBindings: []deploy.ReportBinding{
				{Report: "Salgsrapport", Model: "SM_Sales", Workspace: "DP - TEST - SemMod"},
				{Report: "HR Dashboard", Model: "SM_HR", Workspace: "DP - TEST - SemMod"},
			},
		},
	}
	results := []deploy.Result{
		{Name: "LH_Gold", Type: "Lakehouse", Action: deploy.ActionCreate, ID: "id1", WorkspaceID: "ws-data"},
		{Name: "nb_transform_sales", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id2", WorkspaceID: "ws-data"},
		{Name: "WH_Config", Type: "Warehouse", Action: deploy.ActionUpdate, ID: "id3", WorkspaceID: "ws-data",
			Warning: "2 definition file(s) in git not deployed — Warehouse publishes as a shell"},
		{Name: "SM_Sales", Type: "SemanticModel", Action: deploy.ActionCreate, ID: "id4", WorkspaceID: "ws-sem"},
		{Name: "Salgsrapport", Type: "Report", Action: deploy.ActionCreate, ID: "id5", WorkspaceID: "ws-sem"},
		{Name: "RPT_Legacy", Type: "Report", Action: deploy.ActionCreate, WorkspaceID: "ws-sem",
			Err: errors.New("Create Report: 400 InvalidRequest — dataset reference unresolved")},
		{Name: "nb_gammel", Type: "Notebook", Action: deploy.ActionDelete, ID: "id6", WorkspaceID: "ws-data"},
	}
	postRuns := []postDeployOutcome{
		{Run: postDeployRun{Name: "nb_ingest_sales", WorkspaceName: "DP - TEST - Data"}, Status: "Completed", Duration: 94 * time.Second},
		{Run: postDeployRun{Name: "PL_refresh_sales", WorkspaceName: "DP - TEST - Data"}, Status: postDeployStatusSkipped},
	}
	ctx := &deployReportContext{
		Customer: "Fabrikam", Environment: "TEST",
		Source: "origin/main @ 92ea579", Baseline: "DEV", Backend: "per-item",
	}
	html := renderDeployReport(groups, results, postRuns, time.Now(), ctx)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
