package cmd

// Env-gated generators for the Remotion feature video's report screenshots —
// all-green Fabrikam-flavoured data through the real renderers, so the video
// always shows the current report design. Skipped unless the env vars point
// somewhere; see docs/video/ (gitignored) for the pipeline.

import (
	"os"
	"testing"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

func TestWriteVideoDeployReport(t *testing.T) {
	out := os.Getenv("FUTILS_VIDEO_SKETCH")
	if out == "" {
		t.Skip("set FUTILS_VIDEO_SKETCH=<path> to write")
	}
	wsData := fabric.Workspace{ID: "ws-data", DisplayName: "DW - TEST - Data"}
	wsSem := fabric.Workspace{ID: "ws-sem", DisplayName: "DW - TEST - SemMod"}
	groups := []deployGroup{
		{
			Folder: "Backend", Target: wsData,
			Changes: []deploy.RebindChange{
				{Kind: "Lakehouse", Name: "LH_Silver", Old: "3f2ab4c1-88d2-4e01-9c55-72aa01b3fd12", New: "9d41c7e8-52f0-47b3-a1c9-08b64de52d77"},
				{Kind: "SQL endpoint", Name: "LH_Silver", Old: "kx2mdpq7lrguvfobwq33aehk4u.datawarehouse.fabric.microsoft.com", New: "t7pvz2ncw4bfjcqm5hqk6lu2ma.datawarehouse.fabric.microsoft.com"},
				{Kind: "Workspace", Name: "DW - TEST - Data", Old: "5a17c9de-30b4-4f8e-bd26-91c07aa4e1f3", New: "c2e84b06-77d1-4a92-8f35-6bd90211c8a4"},
			},
			Diffs: []ItemDiff{
				{
					Name: "nb_stage_sales", Type: "Notebook",
					Parts: []deploy.PartDiff{{
						Path: "notebook-content.py",
						Old:  "# Fabric notebook source\n\ndf = spark.read.table(\"raw_sales\")\ndf.write.mode(\"overwrite\").saveAsTable(\"stage_sales\")",
						New:  "# Fabric notebook source\n\ndf = spark.read.table(\"raw_sales\")\ndf = df.filter(df.amount > 0)\ndf = df.withColumn(\"loaded_at\", current_timestamp())\ndf.write.mode(\"overwrite\").saveAsTable(\"stage_sales\")",
					}},
				},
				{
					Name: "nb_transform_orders", Type: "Notebook",
					Parts: []deploy.PartDiff{{
						Path: "notebook-content.py",
						Old:  "# Fabric notebook source\n\norders = spark.read.table(\"stage_orders\")\norders.write.mode(\"overwrite\").saveAsTable(\"gold_orders\")",
						New:  "# Fabric notebook source\n\norders = spark.read.table(\"stage_orders\")\norders = orders.dropDuplicates([\"order_id\"])\norders = orders.join(customers, \"customer_id\", \"left\")\norders.write.mode(\"overwrite\").saveAsTable(\"gold_orders\")",
					}},
				},
				{
					Name: "PL_refresh_sales", Type: "DataPipeline",
					Parts: []deploy.PartDiff{{
						Path: "pipeline-content.json",
						Old:  `{"properties":{"activities":[{"name":"Ingest","type":"TridentNotebook","policy":{"timeout":"0.02:00:00","retry":0}}]}}`,
						New:  `{"properties":{"activities":[{"name":"Ingest","type":"TridentNotebook","policy":{"timeout":"0.02:00:00","retry":2}},{"name":"Refresh SM_Sales","type":"SemanticModelRefresh","dependsOn":[{"activity":"Ingest","dependencyConditions":["Succeeded"]}]}]}}`,
					}},
				},
			},
		},
		{
			Folder: "Frontend", Target: wsSem,
			ReportBindings: []deploy.ReportBinding{
				{Report: "Salgsrapport", Model: "SM_Sales", Workspace: "DW - TEST - SemMod"},
			},
		},
	}
	results := []deploy.Result{
		{Name: "LH_Silver", Type: "Lakehouse", Action: deploy.ActionCreate, ID: "i1", WorkspaceID: "ws-data"},
		{Name: "LH_Gold", Type: "Lakehouse", Action: deploy.ActionCreate, ID: "i2", WorkspaceID: "ws-data"},
		{Name: "nb_stage_sales", Type: "Notebook", Action: deploy.ActionUpdate, ID: "i3", WorkspaceID: "ws-data"},
		{Name: "nb_transform_orders", Type: "Notebook", Action: deploy.ActionUpdate, ID: "i4", WorkspaceID: "ws-data"},
		{Name: "PL_refresh_sales", Type: "DataPipeline", Action: deploy.ActionUpdate, ID: "i5", WorkspaceID: "ws-data"},
		{Name: "PL_master", Type: "DataPipeline", Action: deploy.ActionCreate, ID: "i6", WorkspaceID: "ws-data"},
		{Name: "ENV_Spark", Type: "Environment", Action: deploy.ActionUpdate, ID: "i7", WorkspaceID: "ws-data"},
		{Name: "SM_Sales", Type: "SemanticModel", Action: deploy.ActionCreate, ID: "i8", WorkspaceID: "ws-sem"},
		{Name: "Salgsrapport", Type: "Report", Action: deploy.ActionCreate, ID: "i9", WorkspaceID: "ws-sem"},
	}
	postRuns := []postDeployOutcome{
		{Run: postDeployRun{Name: "nb_ingest_sales", WorkspaceName: "DW - TEST - Data"}, Status: "Completed", Duration: 94 * time.Second},
		{Run: postDeployRun{Name: "PL_refresh_sales", WorkspaceName: "DW - TEST - Data"}, Status: "Completed", Duration: 48 * time.Second},
	}
	ctx := &deployReportContext{
		Customer: "Fabrikam", Environment: "TEST",
		Source: "origin/main @ 4f21c8a", Baseline: "DEV",
	}
	html := renderDeployReport(groups, results, postRuns, time.Now(), ctx)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriteVideoSchemaCompareReport mirrors the 05-schemacompare terminal
// clip's data exactly, so the HTML scene continues the same story.
func TestWriteVideoSchemaCompareReport(t *testing.T) {
	out := os.Getenv("FUTILS_VIDEO_SC_SKETCH")
	if out == "" {
		t.Skip("set FUTILS_VIDEO_SC_SKETCH=<path> to write")
	}
	diffs := []schemacompare.LakehouseDiff{
		{
			Lakehouse: "LH_Bronze", Schemas: []string{"dbo", "staging"}, Matching: 3,
			Tables: []schemacompare.TableDiff{
				{Schema: "dbo", Table: "sales_orders", Kind: schemacompare.TableChanged, Columns: []schemacompare.ColumnChange{
					{Name: "discount_pct", Kind: schemacompare.ColAdded, NewType: "decimal(5,2)"},
					{Name: "quantity", Kind: schemacompare.ColTypeChanged, OldType: "int", NewType: "bigint"},
				}},
				{Schema: "staging", Table: "raw_sales_2026", Kind: schemacompare.TableNew},
			},
		},
		{Lakehouse: "LH_Silver", Schemas: []string{"dbo"}, Matching: 2},
	}
	html := renderSchemaCompareReport("DW - DEV - Config", "DW - TEST - Config", diffs)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
}
