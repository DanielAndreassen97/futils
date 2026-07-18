package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Result is the per-item outcome of a deploy run.
type Result struct {
	Name        string
	Type        string
	Action      Action
	ID          string // deployed item ID
	WorkspaceID string // target workspace the item was deployed to / deleted from
	Err         error
	Warning     string // non-fatal issue after a successful publish (e.g. description not synced)
}

// refKind classifies a report's definition.pbir dataset reference.
type refKind int

const (
	refNone         refKind = iota // no pbir, or pbir with no dataset reference — nothing to rebind
	refByPath                      // datasetReference.byPath → a local model folder (rebindable by name)
	refByConnection                // datasetReference.byConnection → a live connection; rebound post-deploy when the model is co-deployed this run, otherwise bound by the in-payload rewrite
)

// datasetRef is the parsed result of a report's definition.pbir dataset
// reference. ModelName is the model's display name — resolved from the byPath
// folder name, or (for byConnection) authoritatively from the reference's model
// GUID via the rebinder (override > baseline-index > flat connectionString
// name); empty when the shape carries no usable name. It is matched against
// modelsByWS to bind a co-deployed model, identically for both shapes.
type datasetRef struct {
	Kind      refKind
	ModelName string
}

// PendingReportRebind defers a single report's rebind to the post-deploy pass.
// It carries everything RebindReports needs without re-reading the plan: which
// workspace the report landed in, its deployed GUID + name, and the parsed
// dataset reference. Because the rebind runs AFTER every group has deployed
// (and against a model map accumulated across all of them), it no longer
// depends on whether the model's group ran before or after the report's.
type PendingReportRebind struct {
	WorkspaceID string
	ReportID    string // the report's deployed GUID
	ReportName  string
	Ref         datasetRef
}

// ReportRebindOutcome is the result of attempting one pending report rebind.
// Exactly one of Err / Warning is set when something noteworthy happened; an
// empty outcome (both blank) means the rebind succeeded cleanly. ReportID
// matches the report's deployed GUID so callers can fold the outcome back into
// its Result. (Distinct from RebindOutcome, which is the lakehouse/workspace
// substitution summary from the rebind package.)
type ReportRebindOutcome struct {
	ReportID string
	Err      error
	Warning  string
}

// Execute publishes a plan against the target workspace. For each item, in
// order, it applies logicalId substitution to every part; when rb is non-nil,
// it also auto-rebinds notebook lakehouse references by name. It then encodes
// parts to base64 and creates or updates the item.
//
// modelsByWS is a caller-owned accumulator (targetWorkspaceID → model
// displayName → deployed GUID): Execute records every published SemanticModel
// into it so the SAME map, threaded through every group's Execute call, is
// complete by the time report rebinds run. Report rebinds are NOT done inline
// (that made them order-dependent); instead each published Report is returned
// as a PendingReportRebind for the post-deploy RebindReports pass.
//
// A per-item error is captured in its Result (the run continues); Execute only
// returns a top-level error for a setup failure that aborts everything.
//
// done, when non-nil, is incremented atomically once per plan item processed
// (success or failure) so a spinner can show live "Publishing X/Y" progress.
// The counter advances even for items that error out, matching the publish
// loop's "we're done with this item, on to the next" semantics.
func Execute(client FabricClient, token string, target fabric.Workspace, plan []PlannedItem, rb *Rebinder, modelsByWS map[string]map[string]string, done *int64) ([]Result, []PendingReportRebind, error) {
	resolver := NewResolver(client, token, target)
	idMap := map[string]string{} // logicalId -> deployed GUID
	results := make([]Result, 0, len(plan))
	var pending []PendingReportRebind

	// markDone bumps the live progress counter once per item; deferred inside the
	// per-item closure so it fires on every exit path (build error, publish error,
	// or success) without repeating it at each `continue`.
	markDone := func() {
		if done != nil {
			atomic.AddInt64(done, 1)
		}
	}

	// Reproduce the repo's directory structure as workspace folders before
	// publishing, so newly-created items land where the repo says. Best-effort:
	// if folder setup fails, publishing continues at the workspace root and a
	// warning rides on the affected items (a folder isn't worth aborting a
	// deploy). Existing items are never moved — only ActionCreate consults it.
	folderIDByPath, folderErr := ensureWorkspaceFolders(client, token, target.ID, plan)

	for _, p := range plan {
		func() {
			defer markDone()
			res := Result{Name: p.Item.DisplayName, Type: p.Item.Type, Action: p.Action, WorkspaceID: target.ID}

			def, parts, err := buildDefinition(p.Item, idMap, resolver, rb)
			if err != nil {
				res.Err = err
				results = append(results, res)
				return
			}

			var deployedID string
			switch p.Action {
			case ActionUpdate:
				// def is nil for a zero-part item (Warehouse, SQLDatabase, bare
				// Lakehouse): there is no definition to push, and the API rejects an
				// empty parts collection — skip straight to the metadata sync below.
				if def != nil {
					err = client.UpdateItemDefinition(token, target.ID, p.ExistingID, def)
				}
				deployedID = p.ExistingID
			default:
				folderID := folderIDByPath[p.WorkspaceFolder]
				if p.WorkspaceFolder != "" && folderID == "" && folderErr != nil {
					res.Warning = joinWarning(res.Warning, fmt.Sprintf("workspace folder %q unavailable (%v) — created at root", p.WorkspaceFolder, folderErr))
				}
				var created fabric.Item
				created, err = client.CreateItem(token, target.ID, p.Item.DisplayName, p.Item.Type, def, p.Item.CreationPayload, folderID)
				deployedID = created.ID
			}
			if err != nil {
				res.Err = fmt.Errorf("%s %s: %w", p.Action, p.Item.Type, err)
				results = append(results, res)
				return
			}
			res.ID = deployedID

			// A freshly created lakehouse provisions its SQL analytics endpoint
			// asynchronously (~15s observed live). Later items in this same run may
			// resolve $sqlendpoint against it (semantic-model rebind, substitutions),
			// so block here until it's up — mirrors fabric-cicd. Timeout is a
			// warning, not an error: the lakehouse itself deployed fine.
			if p.Action != ActionUpdate && p.Item.Type == "Lakehouse" {
				if werr := waitLakehouseSQLEndpoint(client, token, target.ID, deployedID); werr != nil {
					res.Warning = werr.Error()
				}
			}

			// Item metadata (description) lives in .platform, which is never part of
			// the published definition — set it explicitly so git stays the source of
			// truth for descriptions, mirroring fabric-cicd. A failure here is
			// non-fatal: the definition is already published, so it's recorded as a
			// Warning (not Err) — the item still counts as deployed, and a real
			// failure later (e.g. a report rebind) can still set Err.
			if err := client.UpdateItem(token, target.ID, deployedID, p.Item.DisplayName, p.Item.Description); err != nil {
				res.Warning = joinWarning(res.Warning, fmt.Sprintf("description not synced: %v", err))
			}

			if p.Item.LogicalID != "" {
				idMap[p.Item.LogicalID] = deployedID
			}
			if p.Item.Type == "SemanticModel" && modelsByWS != nil {
				if modelsByWS[target.ID] == nil {
					modelsByWS[target.ID] = map[string]string{}
				}
				modelsByWS[target.ID][p.Item.DisplayName] = deployedID
			}
			if p.Item.Type == "Report" {
				// Parse the ref from the SUBSTITUTED parts (what was actually
				// published), not the raw git content — a custom substitution may
				// have rewritten the model reference to its target-environment name.
				pending = append(pending, PendingReportRebind{
					WorkspaceID: target.ID,
					ReportID:    deployedID,
					ReportName:  p.Item.DisplayName,
					Ref:         reportDatasetRef(p.Item, parts, rb),
				})
			}
			results = append(results, res)
		}()
	}
	return results, pending, nil
}

// RebindReports runs AFTER every group has deployed, repointing each published
// report at the matching semantic model. It is order-independent: modelsByWS is
// the union of models published across every group, keyed by target workspace,
// so a model and its report can sit in different folders/groups and still bind —
// while a model named "X" in one workspace can never bind a report in another
// (the workspace key isolates them).
//
// rebinderActive signals whether a Rebinder is configured for this run (rb != nil
// at the call site). This governs the byConnection warning:
//   - byPath, model found in the report's workspace → RebindReport; an API
//     failure becomes an Err outcome.
//   - byPath, model NOT in modelsByWS → silent skip. The most common case is an
//     incremental deploy where only the report changed and its SemanticModel was
//     Unchanged (never published this run); the model is already live and the
//     report is already correctly bound — emitting a Warning here is a false alarm.
//   - byConnection, model CREATED this run (in modelsByWS) → RebindReport; the
//     payload kept the baseline GUID because the model wasn't in the pre-deploy
//     target index at diff time, so we bind it now (mirrors byPath).
//   - byConnection, rebinderActive=false, model not this run → warn; the payload
//     is unchanged (no rebinder ran the in-payload rewrite) so the report retains
//     its stale dev binding — the operator must verify manually.
//   - byConnection, rebinderActive=true, model not this run → no-op; the
//     in-payload rewrite (RebindReportConnection) already resolved it, or it was
//     surfaced as unresolved pre-deploy.
//   - no dataset reference → no outcome (nothing to rebind).
func RebindReports(client FabricClient, token string, modelsByWS map[string]map[string]string, pending []PendingReportRebind, rebinderActive bool) []ReportRebindOutcome {
	var outcomes []ReportRebindOutcome
	for _, pr := range pending {
		switch pr.Ref.Kind {
		case refByPath:
			datasetID, ok := modelsByWS[pr.WorkspaceID][pr.Ref.ModelName]
			if !ok {
				// Model was not published in this run (e.g. Unchanged) — the report
				// is already correctly bound in the target; skip silently.
				continue
			}
			if err := client.RebindReport(token, pr.WorkspaceID, pr.ReportID, datasetID); err != nil {
				outcomes = append(outcomes, ReportRebindOutcome{
					ReportID: pr.ReportID,
					Err:      fmt.Errorf("published but rebind failed: %w", err),
				})
			}
		case refByConnection:
			datasetID, found := modelsByWS[pr.WorkspaceID][pr.Ref.ModelName]
			if found {
				// Model was created this run: the payload kept the baseline GUID
				// because it wasn't in the pre-deploy target index at diff time.
				// Rebind now, mirroring the byPath same-run case.
				if err := client.RebindReport(token, pr.WorkspaceID, pr.ReportID, datasetID); err != nil {
					outcomes = append(outcomes, ReportRebindOutcome{
						ReportID: pr.ReportID,
						Err:      fmt.Errorf("published but rebind failed: %w", err),
					})
				}
			} else if !rebinderActive {
				// No rebinder ran, so the payload rewrite never executed and the
				// report retains its stale dev binding — warn so the operator can
				// verify the binding in the target workspace.
				outcomes = append(outcomes, ReportRebindOutcome{
					ReportID: pr.ReportID,
					Warning:  "report uses a byConnection dataset reference and no rebinder is configured — verify the binding in the target",
				})
			}
			// else (rebinder active, model not published this run): the in-payload
			// rewrite handled it, or it was surfaced as unresolved pre-deploy.
		case refNone:
			// No dataset reference to rebind — nothing to report.
		}
	}
	return outcomes
}

// buildDefinition applies logicalId + rebind substitution to each text part and
// base64-encodes them into a fabric.Definition. It also returns the substituted
// raw parts (path → bytes) so the caller can inspect what was actually
// published (the pending report rebind parses its dataset ref from them).
// Unresolved references are intentionally discarded here — the dry-run surfaces
// them; the publish path leaves any unresolved (cosmetic) GUID as-is.
//
// A zero-part item yields a NIL definition: the items API rejects an empty
// parts collection ("Parts: Must be a non-empty collection"), so such items
// are created as shells (no definition field) and never definition-updated.
func buildDefinition(item LocalItem, idMap map[string]string, resolver *Resolver, rb *Rebinder) (*fabric.Definition, map[string][]byte, error) {
	parts, _, err := SubstituteParts(item, idMap, resolver, rb)
	if err != nil {
		return nil, nil, err
	}
	if len(item.Parts) == 0 {
		return nil, parts, nil
	}
	def := &fabric.Definition{Format: definitionFormat(item)}
	for _, part := range orderedParts(item) {
		def.Parts = append(def.Parts, fabric.DefinitionPart{
			Path:        part.Path,
			Payload:     base64.StdEncoding.EncodeToString(parts[part.Path]),
			PayloadType: "InlineBase64",
		})
	}
	return def, parts, nil
}

// sqlEndpointPollInterval / sqlEndpointWaitAttempts pace waitLakehouseSQLEndpoint:
// ~2 minutes at 5s per attempt (provisioning was ~15s in live testing, but library
// resolution can stretch it). Vars so tests can collapse the wait.
var (
	sqlEndpointPollInterval = 5 * time.Second
	sqlEndpointWaitAttempts = 24
)

// waitLakehouseSQLEndpoint polls a freshly created lakehouse until its SQL
// analytics endpoint is provisioned (GetLakehouseSqlEndpoint succeeds). Any
// error — "still provisioning" or transient — just means another attempt;
// exhausting the attempts returns the last error wrapped as a warning-grade
// failure so the caller can surface it without failing the publish.
func waitLakehouseSQLEndpoint(client FabricClient, token, wsID, lakehouseID string) error {
	var lastErr error
	for i := 0; i < sqlEndpointWaitAttempts; i++ {
		if i > 0 {
			time.Sleep(sqlEndpointPollInterval)
		}
		if _, _, err := client.GetLakehouseSqlEndpoint(token, wsID, lakehouseID); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("SQL endpoint not confirmed after create: %v", lastErr)
}

// definitionFormat returns the definition-envelope format flag for item types
// with several definition variants: an .ipynb notebook must declare "ipynb"
// (the API otherwise parses the payload as the .py git form), and
// SparkJobDefinition always uses "SparkJobDefinitionV2" (mirrors fabric-cicd's
// API_FORMAT_MAPPING). Empty for everything else.
func definitionFormat(item LocalItem) string {
	switch item.Type {
	case "Notebook":
		for _, p := range item.Parts {
			if strings.HasSuffix(p.Path, ".ipynb") {
				return "ipynb"
			}
		}
	case "SparkJobDefinition":
		return "SparkJobDefinitionV2"
	}
	return ""
}

// orderedParts returns the item's parts in publish order. The notebook API
// processes parts in payload order and needs the content file before any
// settings .json (fabric-cicd #869): content first, then other files, then
// *.json — stable within each bucket, so discovery order still breaks ties.
// Every other type keeps discovery order untouched.
func orderedParts(item LocalItem) []Part {
	if item.Type != "Notebook" {
		return item.Parts // preserve discovery order
	}
	prio := func(p Part) int {
		switch {
		case strings.HasSuffix(p.Path, ".py"), strings.HasSuffix(p.Path, ".ipynb"):
			return 0
		case strings.HasSuffix(p.Path, ".json"):
			return 2
		default:
			return 1
		}
	}
	out := append([]Part(nil), item.Parts...)
	sort.SliceStable(out, func(i, j int) bool { return prio(out[i]) < prio(out[j]) })
	return out
}

// reportDatasetRef parses a report's definition.pbir dataset reference once and
// classifies it three ways so the caller can act distinctly on each (instead of
// the old "" that conflated byConnection with no-reference and silently skipped
// both):
//   - byPath → refByPath with the model display name (e.g. "../MyModel.SemanticModel" → "MyModel").
//   - byConnection present → refByConnection, with ModelName resolved from the
//     reference's model GUID via the rebinder (override > baseline-index > flat
//     connectionString name) — the structured shape carries no name of its own,
//     and the flat shape's "initial catalog" can be stale, so the GUID wins.
//   - neither / no pbir / unparseable → refNone (nothing to rebind).
//
// parts, when non-nil, holds the SUBSTITUTED part bytes (from buildDefinition):
// the ref is parsed from what was actually published, so custom substitutions
// that rewrite the model reference are honored. rb may be nil (no rebinder).
func reportDatasetRef(report LocalItem, parts map[string][]byte, rb *Rebinder) datasetRef {
	for _, part := range report.Parts {
		if path.Base(part.Path) != "definition.pbir" {
			continue
		}
		content := part.Content
		if sub, ok := parts[part.Path]; ok {
			content = sub
		}
		return pbirDatasetRef(content, rb)
	}
	return datasetRef{Kind: refNone}
}

// pbirDatasetRef classifies one definition.pbir document. See reportDatasetRef.
func pbirDatasetRef(content []byte, rb *Rebinder) datasetRef {
	var pbir struct {
		DatasetReference struct {
			ByPath struct {
				Path string `json:"path"`
			} `json:"byPath"`
			ByConnection json.RawMessage `json:"byConnection"`
		} `json:"datasetReference"`
	}
	if err := json.Unmarshal(content, &pbir); err != nil {
		return datasetRef{Kind: refNone}
	}
	if ref := pbir.DatasetReference.ByPath.Path; ref != "" {
		base := path.Base(ref) // "MyModel.SemanticModel"
		return datasetRef{Kind: refByPath, ModelName: strings.TrimSuffix(base, ".SemanticModel")}
	}
	if len(pbir.DatasetReference.ByConnection) > 0 && string(pbir.DatasetReference.ByConnection) != "null" {
		// Resolve the model display name so the post-deploy pass can match a
		// same-run-created model in modelsByWS (mirrors byPath). Both shapes carry
		// a model GUID; the rebinder translates it authoritatively (override >
		// baseline index > flat connectionString name). Without a rebinder only the
		// flat connectionString name is available.
		var bc struct {
			ConnectionString     string `json:"connectionString"`
			PbiModelDatabaseName string `json:"pbiModelDatabaseName"`
		}
		_ = json.Unmarshal(pbir.DatasetReference.ByConnection, &bc)
		name, guid := parseFlatConn(bc.ConnectionString)
		if guid == "" {
			guid = bc.PbiModelDatabaseName
		}
		modelName := name
		if rb != nil {
			modelName = rb.resolveModelName(name, guid)
		}
		return datasetRef{Kind: refByConnection, ModelName: modelName}
	}
	return datasetRef{Kind: refNone}
}
