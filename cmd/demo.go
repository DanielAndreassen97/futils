package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// Demo mode is a self-contained fake Fabric tenant for the fictional customer
// "Fabrikam": every flow — run, refresh, move, deploy, schema compare — works
// offline with plausible latencies and a small, coherent dataset. No network,
// no auth, no real tenant. Enable with FUTILS_DEMO=1; seed the matching config
// and git repo with `futils demoseed` (see demoseed.go).
//
// The dataset tells one story across three environments (DEV → TEST → PROD):
// DEV is the baseline the git repo represents, TEST lags one "release" behind
// (an older notebook, a model missing a measure, a report missing a visual,
// one orphan notebook), so a DEV→TEST deploy shows every compare class and
// rebind kind without any manufactured errors.

// EnableDemoMode swaps the real Fabric API for the demo tenant. Called from
// main when FUTILS_DEMO is set, before any command runs.
func EnableDemoMode() {
	DefaultAPI = newDemoClient()
	// The env-publish wait always sleeps a full interval before the first
	// poll (a live tenant's publishDetails.state can be stale right after
	// submit). The demo tenant has no such race, and a 10s stall would
	// dominate every recorded deploy — pace the poll to the fake instead.
	envPublishPollInterval = 500 * time.Millisecond
}

// ── identity ───────────────────────────────────────────────────────────────

var demoEnvs = []string{"DEV", "TEST", "PROD"}

// demoGUID derives a stable, realistic-looking UUID from its inputs (FNV-128a
// with RFC 4122 version/variant nibbles). Deterministic so the seeded git repo
// (demoseed.go) and the fake tenant agree on every GUID without a lookup table.
func demoGUID(parts ...string) string {
	h := fnv.New128a()
	h.Write([]byte(strings.Join(parts, "|")))
	b := h.Sum(nil)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func demoConfigWS(env string) string { return "DW - " + env + " - Config" }
func demoSemModWS(env string) string { return "DW - " + env + " - SemMod" }

// demoSQLHost is the fake SQL analytics endpoint host for an environment,
// shaped like the real ones (opaque hash subdomain).
func demoSQLHost(env string) string {
	h := fnv.New64a()
	h.Write([]byte("sqlhost|" + env))
	return fmt.Sprintf("%x%x.datawarehouse.fabric.microsoft.com", h.Sum64(), h.Sum64()>>13)
}

func demoWorkspaces() []fabric.Workspace {
	var out []fabric.Workspace
	for _, env := range demoEnvs {
		for _, name := range []string{demoConfigWS(env), demoSemModWS(env)} {
			out = append(out, fabric.Workspace{ID: demoGUID("workspace", name), DisplayName: name})
		}
	}
	return out
}

// demoEnvOfWS resolves a workspace ID back to its environment and kind
// ("Config"/"SemMod"). ok=false for unknown IDs.
func demoEnvOfWS(wsID string) (env, kind string, ok bool) {
	for _, e := range demoEnvs {
		if wsID == demoGUID("workspace", demoConfigWS(e)) {
			return e, "Config", true
		}
		if wsID == demoGUID("workspace", demoSemModWS(e)) {
			return e, "SemMod", true
		}
	}
	return "", "", false
}

func demoItemGUID(itemType, name, env string) string {
	return demoGUID("item", itemType, name, env)
}

// demoItems lists a workspace's items. TEST lags the repo: nb_transform_orders
// doesn't exist there yet (deploys as New) and nb_legacy_export exists ONLY
// there (surfaces as an Orphan). SQLEndpoint twins are listed alongside their
// lakehouses — like the real API — which also feeds the baseline name index the
// endpoint GUIDs that Direct-Lake-on-SQL models bake into expressions.
func demoItems(wsID string) ([]fabric.Item, bool) {
	env, kind, ok := demoEnvOfWS(wsID)
	if !ok {
		return nil, false
	}
	mk := func(name, typ string) fabric.Item {
		return fabric.Item{ID: demoItemGUID(typ, name, env), DisplayName: name, Type: typ, WorkspaceID: wsID}
	}
	sqlEndpoint := func(name string) fabric.Item {
		return fabric.Item{ID: demoGUID("sqlendpoint", name, env), DisplayName: name, Type: "SQLEndpoint", WorkspaceID: wsID}
	}
	if kind == "Config" {
		items := []fabric.Item{
			mk("VL_Settings", "VariableLibrary"),
			mk("ENV_Spark", "Environment"),
			mk("LH_Bronze", "Lakehouse"), sqlEndpoint("LH_Bronze"),
			mk("LH_Silver", "Lakehouse"), sqlEndpoint("LH_Silver"),
			mk("nb_ingest_sales", "Notebook"),
			mk("nb_quality_checks", "Notebook"),
			mk("PL_refresh_sales", "DataPipeline"),
		}
		// TEST lags the repo: transform doesn't exist there yet (deploys as
		// New) and only TEST still has the legacy export (the Orphan).
		if env == "TEST" {
			items = append(items, mk("nb_legacy_export", "Notebook"))
		} else {
			items = append(items, mk("nb_transform_orders", "Notebook"))
		}
		return items, true
	}
	return []fabric.Item{
		mk("Sales Model", "SemanticModel"),
		mk("Finance Model", "SemanticModel"),
		mk("Sales Overview", "Report"),
	}, true
}

// ── item content (shared with demoseed.go) ────────────────────────────────
//
// Every builder is parameterized by environment (which GUID family to bake in)
// and freshness: the git repo carries the CURRENT content with DEV GUIDs, the
// TEST/PROD tenant serves the PREVIOUS release, so compare shows real diffs.

func demoNotebookIngest(env string, current bool) string {
	qualityGate := ""
	if current {
		qualityGate = `
# CELL ********************

df = df.dropDuplicates(["order_id"]).filter("order_date is not null")
row_count = df.count()
assert row_count > 0, "landing extract is empty"
`
	}
	return demoNotebookHeader(env) + `
# CELL ********************

df = (spark.read.format("parquet")
    .load("Files/landing/sales_orders"))
` + qualityGate + `
# CELL ********************

df.write.mode("overwrite").saveAsTable("bronze.sales_orders")
`
}

func demoNotebookTransform(env string) string {
	return demoNotebookHeader(env) + `
# CELL ********************

orders = spark.read.table("bronze.sales_orders")
customers = spark.read.table("bronze.customers")

# CELL ********************

enriched = (orders.join(customers, "customer_id", "left")
    .withColumn("order_value", orders.quantity * orders.unit_price))
enriched.write.mode("overwrite").saveAsTable("silver.orders_enriched")
`
}

func demoNotebookQuality(env string) string {
	return demoNotebookHeader(env) + `
# CELL ********************

for table in ["bronze.sales_orders", "bronze.customers"]:
    count = spark.read.table(table).count()
    print(f"{table}: {count} rows")
    assert count > 0, f"{table} is empty"
`
}

// demoNotebookHeader is the Fabric .py notebook preamble with the lakehouse
// dependency block the rebinder translates (default lakehouse + workspace).
func demoNotebookHeader(env string) string {
	return `# Fabric notebook source

# METADATA ********************

# META {
# META   "kernel_info": {
# META     "name": "synapse_pyspark"
# META   },
# META   "dependencies": {
# META     "lakehouse": {
# META       "default_lakehouse": "` + demoItemGUID("Lakehouse", "LH_Bronze", env) + `",
# META       "default_lakehouse_name": "LH_Bronze",
# META       "default_lakehouse_workspace_id": "` + demoGUID("workspace", demoConfigWS(env)) + `"
# META     }
# META   }
# META }
`
}

// demoSalesModelTMDL is a Direct Lake on OneLake model; current adds a measure.
func demoSalesModelTMDL(env string, current bool) (expressions, model string) {
	expressions = `expression DirectLakeSource =
		AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/` +
		demoGUID("workspace", demoConfigWS(env)) + `/` + demoItemGUID("Lakehouse", "LH_Bronze", env) + `", [HierarchicalNavigation = true])
	lineageTag: ` + demoGUID("lineage", "DirectLakeSource") + `
`
	extra := ""
	if current {
		extra = `
	/// Year-over-year sales growth
	measure 'Sales YoY %' = DIVIDE([Total Sales] - CALCULATE([Total Sales], SAMEPERIODLASTYEAR(DimDate[date])), CALCULATE([Total Sales], SAMEPERIODLASTYEAR(DimDate[date])))
		formatString: 0.0 %;-0.0 %;0.0 %
`
	}
	model = `model Model
	culture: en-US
	defaultPowerBIDataSourceVersion: powerBI_V3

table FaktaSales
	/// Total sales amount across all channels
	measure 'Total Sales' = SUM(FaktaSales[amount])
		formatString: #,0
` + extra
	return expressions, model
}

// demoFinanceModelTMDL is a Direct Lake on SQL model (endpoint host + GUID
// form). Identical on both sides apart from the environment GUIDs — after
// rebinding it compares as Unchanged, which is exactly the point.
func demoFinanceModelTMDL(env string) (expressions, model string) {
	expressions = `expression DatabaseQuery =
		let
		    database = Sql.Database("` + demoSQLHost(env) + `", "` + demoGUID("sqlendpoint", "LH_Silver", env) + `")
		in
		    database
	lineageTag: ` + demoGUID("lineage", "DatabaseQuery") + `
`
	model = `model Model
	culture: en-US
	defaultPowerBIDataSourceVersion: powerBI_V3

table fact_margins
	/// Gross margin across product lines
	measure 'Gross Margin' = SUM(fact_margins[margin])
		formatString: #,0.00
`
	return expressions, model
}

// demoReportPBIR binds Sales Overview to the environment's Sales Model in the
// XMLA byConnection shape the rebinder rewrites.
func demoReportPBIR(env string) string {
	smGUID := demoItemGUID("SemanticModel", "Sales Model", env)
	conn := fmt.Sprintf(`Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/%s\";initial catalog=\"Sales Model\";integrated security=ClaimsToken;semanticmodelid=%s`, demoSemModWS(env), smGUID)
	return `{
  "version": "1.0",
  "datasetReference": {
    "byConnection": {
      "connectionString": "` + conn + `",
      "pbiServiceModelId": null,
      "pbiModelVirtualServerName": "sobe_wowvirtualserver",
      "pbiModelDatabaseName": "` + smGUID + `",
      "name": "EntityDataSource",
      "connectionType": "pbiServiceXmlaStyleLive"
    }
  }
}
`
}

func demoReportJSON(current bool) string {
	extraVisual := ""
	if current {
		extraVisual = `,
    { "name": "salesYoyCard", "visualType": "card", "title": "Sales YoY %" }`
	}
	return `{
  "theme": "FabrikamCorporate",
  "pages": [
    {
      "name": "Overview",
      "visuals": [
        { "name": "salesTrend", "visualType": "lineChart", "title": "Sales by month" },
        { "name": "salesByStore", "visualType": "barChart", "title": "Sales by store" }` + extraVisual + `
      ]
    }
  ]
}
`
}

// demoVariableLibrary renders the three definition parts of VL_Settings: the
// variables with their DEV defaults, TEST/PROD value sets overriding them, and
// the settings file whose valueSetsOrder the post-deploy activation reads.
// current adds a variable, so TEST shows a real diff.
func demoVariableLibrary(current bool) (variables, settings string, valueSets map[string]string) {
	extra := ""
	if current {
		extra = `,
    { "name": "full_reload", "type": "Boolean", "value": false, "note": "Force a full reload on next run" }`
	}
	variables = `{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/variableLibrary/definition/variables/1.0.0/schema.json",
  "variables": [
    { "name": "landing_path", "type": "String", "value": "Files/landing", "note": "Root of the raw extracts" },
    { "name": "retention_days", "type": "Integer", "value": 7 }` + extra + `
  ]
}
`
	settings = `{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/variableLibrary/definition/settings/1.0.0/schema.json",
  "valueSetsOrder": [
    "TEST",
    "PROD"
  ]
}
`
	valueSets = map[string]string{
		"TEST": `{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/variableLibrary/definition/valueSet/1.0.0/schema.json",
  "name": "TEST",
  "variableOverrides": [
    { "name": "retention_days", "value": 30 }
  ]
}
`,
		"PROD": `{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/variableLibrary/definition/valueSet/1.0.0/schema.json",
  "name": "PROD",
  "variableOverrides": [
    { "name": "landing_path", "value": "Files/landing-prod" },
    { "name": "retention_days", "value": 365 }
  ]
}
`,
	}
	return variables, settings, valueSets
}

// demoPipeline renders PL_refresh_sales' pipeline-content.json — a small
// copy-then-notebook pipeline. current adds a timeout bump so TEST diffs.
func demoPipeline(env string, current bool) string {
	timeout := "0.02:00:00"
	if current {
		timeout = "0.04:00:00"
	}
	return `{
  "properties": {
    "parameters": {
      "load_date": { "type": "string", "defaultValue": "2026-01-01" },
      "full_reload": { "type": "bool", "defaultValue": false },
      "batch_size": { "type": "int", "defaultValue": 5000 }
    },
    "activities": [
      {
        "name": "Copy landing extracts",
        "type": "Copy",
        "typeProperties": { "source": { "type": "BinarySource" }, "sink": { "type": "BinarySink" } },
        "policy": { "timeout": "` + timeout + `", "retry": 1 }
      },
      {
        "name": "Ingest sales",
        "type": "TridentNotebook",
        "dependsOn": [ { "activity": "Copy landing extracts", "dependencyConditions": [ "Succeeded" ] } ],
        "typeProperties": {
          "notebookId": "` + demoItemGUID("Notebook", "nb_ingest_sales", env) + `",
          "workspaceId": "` + demoGUID("workspace", demoConfigWS(env)) + `"
        }
      }
    ]
  }
}
`
}

// demoShortcuts renders LH_Bronze's shortcuts.metadata.json: one OneLake
// shortcut into LH_Silver (GUIDs in the given env's family, so a DEV→TEST
// deploy rebinds them by name) and one external S3 shortcut (left untouched).
// current bumps the target subpath so the previous release differs — the diff
// then shows a clean path change with the GUIDs already normalized to target.
func demoShortcuts(env string) string { return demoShortcutsAt(env, true) }

func demoShortcutsAt(env string, current bool) string {
	subpath := "Tables/orders"
	if current {
		subpath = "Tables/orders_enriched"
	}
	return `[
  {
    "name": "silver_orders",
    "path": "Tables",
    "target": {
      "type": "OneLake",
      "oneLake": {
        "workspaceId": "` + demoGUID("workspace", demoConfigWS(env)) + `",
        "itemId": "` + demoItemGUID("Lakehouse", "LH_Silver", env) + `",
        "path": "` + subpath + `"
      }
    }
  },
  {
    "name": "vendor_feed",
    "path": "Files",
    "target": {
      "type": "AmazonS3",
      "amazonS3": { "location": "https://vendor-bucket.s3.amazonaws.com", "subpath": "/daily" }
    }
  }
]
`
}

// demoPipelineMaster renders PL_master — an orchestrator that invokes
// PL_refresh_sales by its logicalId, exactly how Fabric git-sync serializes a
// sibling InvokePipeline reference. It exists only in the repo (New in every
// target), so a full deploy demonstrates the dependency ordering: the invoked
// pipeline must be created first for the logicalId substitution to resolve.
func demoPipelineMaster() string {
	return `{
  "properties": {
    "activities": [
      {
        "name": "Run sales refresh",
        "type": "InvokePipeline",
        "typeProperties": {
          "pipelineId": "` + demoGUID("logical", "DataPipeline", "PL_refresh_sales") + `",
          "waitOnCompletion": true
        }
      }
    ]
  }
}
`
}

// demoSparkcompute renders ENV_Spark's Setting/Sparkcompute.yml. current bumps
// the runtime and executor ceiling, so TEST shows a real Environment diff —
// and the post-deploy staging→publish step has something to publish.
func demoSparkcompute(current bool) string {
	runtime, maxExec := "1.2", "6"
	if current {
		runtime, maxExec = "1.3", "9"
	}
	return `enable_native_execution_engine: false
driver_cores: 4
driver_memory: 28g
executor_cores: 4
executor_memory: 28g
dynamic_executor_allocation:
  enabled: true
  min_executors: 1
  max_executors: ` + maxExec + `
runtime_version: ` + runtime + `
`
}

// demoPlatform renders the .platform descriptor for a seeded repo item.
func demoPlatform(itemType, displayName string) string {
	return `{
  "$schema": "https://developer.microsoft.com/json-schemas/fabric/gitIntegration/platformProperties/2.0.0/schema.json",
  "metadata": {
    "type": "` + itemType + `",
    "displayName": "` + displayName + `"
  },
  "config": {
    "version": "2.0",
    "logicalId": "` + demoGUID("logical", itemType, displayName) + `"
  }
}
`
}

// demoRepoFiles returns every file of the seeded git repo, keyed by
// repo-relative path — the CURRENT content, baked with DEV GUIDs.
func demoRepoFiles() map[string]string {
	salesExpr, salesModel := demoSalesModelTMDL("DEV", true)
	finExpr, finModel := demoFinanceModelTMDL("DEV")
	vlVars, vlSettings, vlSets := demoVariableLibrary(true)
	return map[string]string{
		"Backend/VL_Settings.VariableLibrary/.platform":           demoPlatform("VariableLibrary", "VL_Settings"),
		"Backend/VL_Settings.VariableLibrary/variables.json":      vlVars,
		"Backend/VL_Settings.VariableLibrary/settings.json":       vlSettings,
		"Backend/VL_Settings.VariableLibrary/valueSets/TEST.json": vlSets["TEST"],
		"Backend/VL_Settings.VariableLibrary/valueSets/PROD.json": vlSets["PROD"],

		"Backend/LH_Bronze.Lakehouse/.platform":               demoPlatform("Lakehouse", "LH_Bronze"),
		"Backend/LH_Bronze.Lakehouse/shortcuts.metadata.json": demoShortcuts("DEV"),
		"Backend/LH_Silver.Lakehouse/.platform":               demoPlatform("Lakehouse", "LH_Silver"),

		"Backend/ENV_Spark.Environment/.platform":                demoPlatform("Environment", "ENV_Spark"),
		"Backend/ENV_Spark.Environment/Setting/Sparkcompute.yml": demoSparkcompute(true),

		"Backend/PL_refresh_sales.DataPipeline/.platform":             demoPlatform("DataPipeline", "PL_refresh_sales"),
		"Backend/PL_refresh_sales.DataPipeline/pipeline-content.json": demoPipeline("DEV", true),

		"Backend/PL_master.DataPipeline/.platform":             demoPlatform("DataPipeline", "PL_master"),
		"Backend/PL_master.DataPipeline/pipeline-content.json": demoPipelineMaster(),

		// A nested item: lives under Backend/Notebooks/Staging in git, so a
		// deploy to a fresh workspace reproduces that folder path. It's absent
		// from the tenant (see demoItems) → always New → placed in the folder.
		"Backend/Notebooks/Staging/nb_stage_sales.Notebook/.platform":           demoPlatform("Notebook", "nb_stage_sales"),
		"Backend/Notebooks/Staging/nb_stage_sales.Notebook/notebook-content.py": demoNotebookQuality("DEV"),

		"Backend/nb_ingest_sales.Notebook/.platform":               demoPlatform("Notebook", "nb_ingest_sales"),
		"Backend/nb_ingest_sales.Notebook/notebook-content.py":     demoNotebookIngest("DEV", true),
		"Backend/nb_transform_orders.Notebook/.platform":           demoPlatform("Notebook", "nb_transform_orders"),
		"Backend/nb_transform_orders.Notebook/notebook-content.py": demoNotebookTransform("DEV"),
		"Backend/nb_quality_checks.Notebook/.platform":             demoPlatform("Notebook", "nb_quality_checks"),
		"Backend/nb_quality_checks.Notebook/notebook-content.py":   demoNotebookQuality("DEV"),

		"Frontend/Sales Model.SemanticModel/.platform":                   demoPlatform("SemanticModel", "Sales Model"),
		"Frontend/Sales Model.SemanticModel/definition/expressions.tmdl": salesExpr,
		"Frontend/Sales Model.SemanticModel/definition/model.tmdl":       salesModel,

		"Frontend/Finance Model.SemanticModel/.platform":                   demoPlatform("SemanticModel", "Finance Model"),
		"Frontend/Finance Model.SemanticModel/definition/expressions.tmdl": finExpr,
		"Frontend/Finance Model.SemanticModel/definition/model.tmdl":       finModel,

		"Frontend/Sales Overview.Report/.platform":       demoPlatform("Report", "Sales Overview"),
		"Frontend/Sales Overview.Report/definition.pbir": demoReportPBIR("DEV"),
		"Frontend/Sales Overview.Report/report.json":     demoReportJSON(true),
	}
}

// demoDefinition serves the tenant-side definition of one item: the PREVIOUS
// release, in the item's own environment's GUID family.
func demoDefinition(env string, item fabric.Item) *fabric.Definition {
	part := func(path, content string) fabric.DefinitionPart {
		return fabric.DefinitionPart{
			Path:        path,
			Payload:     base64.StdEncoding.EncodeToString([]byte(content)),
			PayloadType: "InlineBase64",
		}
	}
	switch item.Type + "/" + item.DisplayName {
	case "VariableLibrary/VL_Settings":
		vlVars, vlSettings, vlSets := demoVariableLibrary(false)
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("variables.json", vlVars),
			part("settings.json", vlSettings),
			part("valueSets/TEST.json", vlSets["TEST"]),
			part("valueSets/PROD.json", vlSets["PROD"]),
		}}
	case "Environment/ENV_Spark":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("Setting/Sparkcompute.yml", demoSparkcompute(false)),
		}}
	case "DataPipeline/PL_refresh_sales":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("pipeline-content.json", demoPipeline(env, false)),
		}}
	case "Lakehouse/LH_Bronze":
		// Previous release: target-env GUIDs already (a prior deploy rebound
		// them) but the older subpath — so the diff is a clean path change and
		// the shortcut rebind resolves to an unchanged target GUID.
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("shortcuts.metadata.json", demoShortcutsAt(env, false)),
		}}
	case "Notebook/nb_ingest_sales":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{part("notebook-content.py", demoNotebookIngest(env, false))}}
	case "Notebook/nb_transform_orders":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{part("notebook-content.py", demoNotebookTransform(env))}}
	case "Notebook/nb_quality_checks":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{part("notebook-content.py", demoNotebookQuality(env))}}
	case "Notebook/nb_legacy_export":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{part("notebook-content.py", demoNotebookHeader(env)+`
# CELL ********************

spark.read.table("bronze.sales_orders").write.format("csv").save("Files/export/legacy")
`)}}
	case "SemanticModel/Sales Model":
		expr, model := demoSalesModelTMDL(env, false)
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("definition/expressions.tmdl", expr),
			part("definition/model.tmdl", model),
		}}
	case "SemanticModel/Finance Model":
		expr, model := demoFinanceModelTMDL(env)
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("definition/expressions.tmdl", expr),
			part("definition/model.tmdl", model),
		}}
	case "Report/Sales Overview":
		return &fabric.Definition{Parts: []fabric.DefinitionPart{
			part("definition.pbir", demoReportPBIR(env)),
			part("report.json", demoReportJSON(false)),
		}}
	}
	// Lakehouses and endpoints have no definition parts.
	return &fabric.Definition{}
}

// demoIpynb is what the run flow's parameter picker parses: a notebook with a
// Papermill-tagged parameters cell of simple assignments.
func demoIpynb() []byte {
	nb := map[string]any{
		"nbformat": 4, "nbformat_minor": 5,
		"metadata": map[string]any{"language_info": map[string]any{"name": "python"}},
		"cells": []map[string]any{
			{
				"cell_type": "code",
				"metadata":  map[string]any{"tags": []string{"parameters"}},
				"source": []string{
					"run_date = \"2026-07-17\"\n",
					"full_reload = False\n",
					"target_table = \"sales_orders\"\n",
				},
			},
			{
				"cell_type": "code",
				"metadata":  map[string]any{},
				"source":    []string{"df = spark.read.format(\"parquet\").load(f\"Files/landing/{target_table}\")\n"},
			},
		},
	}
	b, _ := json.Marshal(nb)
	return b
}

// ── the fake client ────────────────────────────────────────────────────────

// demoClient implements APIClient against the in-memory tenant. Latencies are
// fixed small sleeps: long enough that spinners visibly spin, short enough
// that a demo stays snappy.
type demoClient struct {
	mu       sync.Mutex
	polls    map[string]int             // job instance URL -> poll count
	envPolls map[string]int             // environment itemID -> publish-state poll count
	folders  map[string][]fabric.Folder // workspaceID -> created folders
}

func newDemoClient() *demoClient {
	return &demoClient{polls: map[string]int{}, envPolls: map[string]int{}, folders: map[string][]fabric.Folder{}}
}

func (c *demoClient) GetAccessToken(profile string) (string, error) {
	time.Sleep(250 * time.Millisecond)
	return "demo-token", nil
}

func (c *demoClient) GetWorkspaceID(token, workspaceName string) (string, error) {
	time.Sleep(300 * time.Millisecond)
	for _, ws := range demoWorkspaces() {
		if ws.DisplayName == workspaceName {
			return ws.ID, nil
		}
	}
	return "", fmt.Errorf("workspace %q not found", workspaceName)
}

func (c *demoClient) ListWorkspaces(token string) ([]fabric.Workspace, error) {
	time.Sleep(400 * time.Millisecond)
	return demoWorkspaces(), nil
}

func (c *demoClient) ListItems(token, workspaceID string) ([]fabric.Item, error) {
	time.Sleep(350 * time.Millisecond)
	items, ok := demoItems(workspaceID)
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", workspaceID)
	}
	return items, nil
}

func (c *demoClient) ListItemsByType(token, workspaceID, itemType string) ([]fabric.Item, error) {
	all, err := c.ListItems(token, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []fabric.Item
	for _, it := range all {
		if it.Type == itemType {
			out = append(out, it)
		}
	}
	return out, nil
}

func (c *demoClient) ListNotebooks(token, workspaceID string) ([]fabric.Item, error) {
	return c.ListItemsByType(token, workspaceID, "Notebook")
}

func (c *demoClient) GetNotebookIpynb(token, workspaceID, itemID string) ([]byte, error) {
	time.Sleep(450 * time.Millisecond)
	return demoIpynb(), nil
}

func (c *demoClient) RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error) {
	time.Sleep(600 * time.Millisecond)
	return "demo://jobs/" + itemID, nil
}

func (c *demoClient) RunPipeline(token, workspaceID, itemID string, params map[string]any) (string, error) {
	time.Sleep(600 * time.Millisecond)
	return "demo://jobs/" + itemID, nil
}

func (c *demoClient) GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error) {
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	c.polls[instanceURL]++
	n := c.polls[instanceURL]
	c.mu.Unlock()
	if n < 2 {
		return fabric.JobInstanceStatus{Status: "InProgress"}, nil
	}
	return fabric.JobInstanceStatus{Status: fabric.JobStatusCompleted}, nil
}

func (c *demoClient) GetItemDefinition(token, workspaceID, itemID, format string) (*fabric.Definition, error) {
	time.Sleep(300 * time.Millisecond)
	env, _, ok := demoEnvOfWS(workspaceID)
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", workspaceID)
	}
	if it, found := demoItemByID(workspaceID, itemID); found {
		return demoDefinition(env, it), nil
	}
	return nil, fmt.Errorf("item %q not found", itemID)
}

func (c *demoClient) CreateItem(token, workspaceID, displayName, itemType string, def *fabric.Definition, creationPayload json.RawMessage, folderID string) (fabric.Item, error) {
	time.Sleep(700 * time.Millisecond)
	return fabric.Item{
		ID:          demoGUID("created", itemType, displayName, workspaceID),
		DisplayName: displayName,
		Type:        itemType,
		WorkspaceID: workspaceID,
	}, nil
}

// demoFolders is the in-memory workspace folder store per workspace, so a demo
// deploy that reproduces repo subfolders shows folders being created once and
// reused on a re-run.
func (c *demoClient) ListFolders(token, workspaceID string) ([]fabric.Folder, error) {
	time.Sleep(200 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fabric.Folder(nil), c.folders[workspaceID]...), nil
}

func (c *demoClient) CreateFolder(token, workspaceID, displayName, parentFolderID string) (fabric.Folder, error) {
	time.Sleep(250 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	f := fabric.Folder{
		ID:             demoGUID("folder", workspaceID, parentFolderID, displayName),
		DisplayName:    displayName,
		ParentFolderID: parentFolderID,
	}
	c.folders[workspaceID] = append(c.folders[workspaceID], f)
	return f, nil
}

func (c *demoClient) UpdateItemDefinition(token, workspaceID, itemID string, def *fabric.Definition) error {
	time.Sleep(650 * time.Millisecond)
	return nil
}

func (c *demoClient) UpdateItem(token, workspaceID, itemID, displayName, description string) error {
	time.Sleep(300 * time.Millisecond)
	return nil
}

func (c *demoClient) DeleteItem(token, workspaceID, itemID string) error {
	time.Sleep(400 * time.Millisecond)
	return nil
}

func (c *demoClient) RebindReport(token, workspaceID, reportID, datasetID string) error {
	time.Sleep(350 * time.Millisecond)
	return nil
}

func (c *demoClient) ListDatasets(token, workspaceID string) ([]fabric.Dataset, error) {
	models, err := c.ListItemsByType(token, workspaceID, "SemanticModel")
	if err != nil {
		return nil, err
	}
	out := make([]fabric.Dataset, len(models))
	for i, m := range models {
		out[i] = fabric.Dataset{ID: m.ID, Name: m.DisplayName}
	}
	return out, nil
}

func (c *demoClient) QueryRefreshableTables(token, workspaceID, datasetID string) ([]string, error) {
	time.Sleep(500 * time.Millisecond)
	return []string{
		"DimCustomer", "DimDate", "DimProduct", "DimStore",
		"FaktaSales", "FaktaOrders", "FaktaInventory",
		"LogRefresh",
	}, nil
}

func (c *demoClient) TriggerRefresh(token, workspaceID, datasetID string, tables []string) (string, error) {
	time.Sleep(550 * time.Millisecond)
	return "demo-refresh-" + datasetID, nil
}

func (c *demoClient) WaitForRefresh(token, workspaceID, datasetID, requestID string) (fabric.RefreshStatus, error) {
	time.Sleep(2200 * time.Millisecond)
	return fabric.RefreshStatus{Status: "Completed"}, nil
}

func (c *demoClient) GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (string, string, error) {
	time.Sleep(250 * time.Millisecond)
	env, _, ok := demoEnvOfWS(workspaceID)
	if !ok {
		return "", "", fmt.Errorf("workspace %q not found", workspaceID)
	}
	if it, found := demoItemByID(workspaceID, lakehouseID); found && it.Type == "Lakehouse" {
		return demoSQLHost(env), demoGUID("sqlendpoint", it.DisplayName, env), nil
	}
	// A lakehouse created earlier in this session isn't in the seed store —
	// in demo-land it provisions instantly, so the deploy's post-create
	// endpoint wait returns on the first poll instead of stalling.
	return demoSQLHost(env), demoGUID("sqlendpoint", lakehouseID, env), nil
}

func (c *demoClient) BulkImportDefinitions(token, workspaceID string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error) {
	time.Sleep(1200 * time.Millisecond)
	var details []fabric.BulkImportDetail
	for _, p := range parts {
		name, typ, ok := demoBulkItem(p)
		if !ok {
			continue
		}
		details = append(details, fabric.BulkImportDetail{
			ItemDisplayName: name,
			ItemType:        typ,
			OperationType:   "Update",
			OperationStatus: "Succeeded",
		})
	}
	return &fabric.BulkImportResult{Details: details}, nil
}

// demoBulkItem identifies the item a bulk part belongs to the same way the
// real backend does: every item's payload carries exactly one .platform part,
// and its metadata names the item — no type list to keep in sync.
func demoBulkItem(p fabric.DefinitionPart) (name, typ string, ok bool) {
	if !strings.HasSuffix(p.Path, "/.platform") {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(p.Payload)
	if err != nil {
		return "", "", false
	}
	var platform struct {
		Metadata struct {
			Type        string `json:"type"`
			DisplayName string `json:"displayName"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &platform) != nil || platform.Metadata.Type == "" {
		return "", "", false
	}
	return platform.Metadata.DisplayName, platform.Metadata.Type, true
}

// SetVariableLibraryActiveSet records nothing — the demo just succeeds after
// a beat, so the post-deploy activation line shows in recordings.
func (c *demoClient) SetVariableLibraryActiveSet(token, workspaceID, itemID, valueSetName string) error {
	time.Sleep(350 * time.Millisecond)
	return nil
}

// PublishEnvironment succeeds after a beat, like the other demo mutations.
func (c *demoClient) PublishEnvironment(token, workspaceID, itemID string) error {
	time.Sleep(400 * time.Millisecond)
	return nil
}

// GetEnvironmentPublishState walks Running → Success in one poll cycle, so the
// deploy's publish spinner gets a visible in-progress phase without stalling a
// demo recording for a full extra poll interval.
func (c *demoClient) GetEnvironmentPublishState(token, workspaceID, itemID string) (string, error) {
	time.Sleep(250 * time.Millisecond)
	c.mu.Lock()
	c.envPolls[itemID]++
	n := c.envPolls[itemID]
	c.mu.Unlock()
	if n < 2 {
		return "running", nil
	}
	return "success", nil
}

// NewOneLakeAPI implements the flow's optional oneLakeProvider interface, so
// schema compare gets the offline fake without a second injection global.
func (c *demoClient) NewOneLakeAPI(string) (schemacompare.OneLakeTableAPI, error) {
	return demoOneLake{}, nil
}

// ── schema compare fake ────────────────────────────────────────────────────

// demoOneLake serves lakehouse table schemas. DEV is one migration ahead of
// TEST on LH_Bronze (a new column, a widened type); LH_Silver is identical.
type demoOneLake struct{}

func (demoOneLake) ListSchemas(wsID, lhID string) ([]string, error) {
	time.Sleep(300 * time.Millisecond)
	if demoLakehouseName(wsID, lhID) == "LH_Bronze" {
		return []string{"dbo", "staging"}, nil
	}
	return []string{"dbo"}, nil
}

func (demoOneLake) ListTables(wsID, lhID, schema string) ([]string, error) {
	time.Sleep(250 * time.Millisecond)
	if demoLakehouseName(wsID, lhID) == "LH_Bronze" {
		if schema == "staging" {
			return []string{"raw_sales"}, nil
		}
		return []string{"customers", "products", "sales_orders"}, nil
	}
	return []string{"fact_margins", "orders_enriched"}, nil
}

func (demoOneLake) GetTable(wsID, lhID, schema, table string) ([]schemacompare.ColumnSchema, error) {
	time.Sleep(200 * time.Millisecond)
	env, _, _ := demoEnvOfWS(wsID)
	col := func(pos int, name, typ string) schemacompare.ColumnSchema {
		return schemacompare.ColumnSchema{Name: name, Type: typ, Nullable: true, Position: pos}
	}
	switch table {
	case "sales_orders":
		cols := []schemacompare.ColumnSchema{
			col(1, "order_id", "string"), col(2, "customer_id", "string"),
			col(3, "order_date", "date"), col(4, "unit_price", "decimal(18,2)"),
		}
		if env == "DEV" {
			// DEV is a migration ahead: widened quantity + a new column.
			cols = append(cols, col(5, "quantity", "bigint"), col(6, "discount_pct", "decimal(5,2)"))
		} else {
			cols = append(cols, col(5, "quantity", "int"))
		}
		return cols, nil
	case "customers":
		return []schemacompare.ColumnSchema{
			col(1, "customer_id", "string"), col(2, "customer_name", "string"),
			col(3, "segment", "string"), col(4, "country", "string"),
		}, nil
	case "products":
		return []schemacompare.ColumnSchema{
			col(1, "product_id", "string"), col(2, "product_name", "string"),
			col(3, "list_price", "decimal(18,2)"),
		}, nil
	case "raw_sales":
		return []schemacompare.ColumnSchema{
			col(1, "payload", "string"), col(2, "ingested_at", "timestamp"),
		}, nil
	case "orders_enriched":
		return []schemacompare.ColumnSchema{
			col(1, "order_id", "string"), col(2, "customer_name", "string"),
			col(3, "order_value", "decimal(18,2)"),
		}, nil
	case "fact_margins":
		return []schemacompare.ColumnSchema{
			col(1, "product_id", "string"), col(2, "margin", "decimal(18,2)"),
		}, nil
	}
	return nil, fmt.Errorf("table %s.%s not found", schema, table)
}

// demoItemByID finds an item in a workspace by its GUID — the shared lookup
// behind definition serving, endpoint resolution, and lakehouse naming.
func demoItemByID(wsID, itemID string) (fabric.Item, bool) {
	items, ok := demoItems(wsID)
	if !ok {
		return fabric.Item{}, false
	}
	for _, it := range items {
		if it.ID == itemID {
			return it, true
		}
	}
	return fabric.Item{}, false
}

// demoLakehouseName reverses a lakehouse ID to its display name.
func demoLakehouseName(wsID, lhID string) string {
	it, _ := demoItemByID(wsID, lhID)
	return it.DisplayName
}
