package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// Result is the per-item outcome of a deploy run.
type Result struct {
	Name    string
	Type    string
	Action  Action
	ID      string // deployed item ID
	Err     error
	Warning string // non-fatal issue after a successful publish (e.g. description not synced)
}

// refKind classifies a report's definition.pbir dataset reference.
type refKind int

const (
	refNone         refKind = iota // no pbir, or pbir with no dataset reference — nothing to rebind
	refByPath                      // datasetReference.byPath → a local model folder (rebindable by name)
	refByConnection                // datasetReference.byConnection → a live connection (not rebindable here)
)

// datasetRef is the parsed result of a report's definition.pbir dataset
// reference. ModelName is only meaningful when Kind == refByPath.
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
// order, it applies logicalId substitution and parameter.yml find_replace to
// every part; when rb is non-nil, it also auto-rebinds notebook lakehouse
// references by name. It then encodes parts to base64 and creates or updates
// the item.
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
func Execute(client FabricClient, token string, target fabric.Workspace, env string, plan []PlannedItem, params Parameters, rb *Rebinder, modelsByWS map[string]map[string]string) ([]Result, []PendingReportRebind, error) {
	resolver := NewResolver(client, token, target)
	idMap := map[string]string{} // logicalId -> deployed GUID
	results := make([]Result, 0, len(plan))
	var pending []PendingReportRebind

	for _, p := range plan {
		res := Result{Name: p.Item.DisplayName, Type: p.Item.Type, Action: p.Action}

		def, err := buildDefinition(p.Item, env, params, idMap, resolver, rb)
		if err != nil {
			res.Err = err
			results = append(results, res)
			continue
		}

		var deployedID string
		switch p.Action {
		case ActionUpdate:
			err = client.UpdateItemDefinition(token, target.ID, p.ExistingID, def)
			deployedID = p.ExistingID
		default:
			var created fabric.Item
			created, err = client.CreateItem(token, target.ID, p.Item.DisplayName, p.Item.Type, def)
			deployedID = created.ID
		}
		if err != nil {
			res.Err = fmt.Errorf("%s %s: %w", p.Action, p.Item.Type, err)
			results = append(results, res)
			continue
		}
		res.ID = deployedID

		// Item metadata (description) lives in .platform, which is never part of
		// the published definition — set it explicitly so git stays the source of
		// truth for descriptions, mirroring fabric-cicd. A failure here is
		// non-fatal: the definition is already published, so it's recorded as a
		// Warning (not Err) — the item still counts as deployed, and a real
		// failure later (e.g. a report rebind) can still set Err.
		if err := client.UpdateItem(token, target.ID, deployedID, p.Item.DisplayName, p.Item.Description); err != nil {
			res.Warning = fmt.Sprintf("description not synced: %v", err)
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
			pending = append(pending, PendingReportRebind{
				WorkspaceID: target.ID,
				ReportID:    deployedID,
				ReportName:  p.Item.DisplayName,
				Ref:         reportDatasetRef(p.Item),
			})
		}
		results = append(results, res)
	}
	return results, pending, nil
}

// RebindReports runs AFTER every group has deployed, repointing each published
// report at the matching semantic model. It is order-independent: modelsByWS is
// the union of models published across every group, keyed by target workspace,
// so a model and its report can sit in different folders/groups and still bind —
// while a model named "X" in one workspace can never bind a report in another
// (the workspace key isolates them). Nothing is silent:
//   - byPath, model found in the report's workspace → RebindReport; an API
//     failure becomes an Err outcome.
//   - byPath, model NOT in that workspace → a Warning (report left on its source
//     dataset), never a silent skip.
//   - byConnection → a Warning (cross-environment rebind unsupported), so the
//     user knows to verify the binding manually (fix #3).
//   - no dataset reference → no outcome (nothing to rebind).
func RebindReports(client FabricClient, token string, modelsByWS map[string]map[string]string, pending []PendingReportRebind) []ReportRebindOutcome {
	var outcomes []ReportRebindOutcome
	for _, pr := range pending {
		switch pr.Ref.Kind {
		case refByPath:
			datasetID, ok := modelsByWS[pr.WorkspaceID][pr.Ref.ModelName]
			if !ok {
				outcomes = append(outcomes, ReportRebindOutcome{
					ReportID: pr.ReportID,
					Warning:  fmt.Sprintf("dataset model %q not found in target workspace — report left bound to its source dataset", pr.Ref.ModelName),
				})
				continue
			}
			if err := client.RebindReport(token, pr.WorkspaceID, pr.ReportID, datasetID); err != nil {
				outcomes = append(outcomes, ReportRebindOutcome{
					ReportID: pr.ReportID,
					Err:      fmt.Errorf("published but rebind failed: %w", err),
				})
			}
		case refByConnection:
			outcomes = append(outcomes, ReportRebindOutcome{
				ReportID: pr.ReportID,
				Warning:  "report uses a byConnection dataset reference — cross-environment dataset rebind is not supported; verify the binding in the target",
			})
		case refNone:
			// No dataset reference to rebind — nothing to report.
		}
	}
	return outcomes
}

// buildDefinition applies logicalId + parameter + rebind substitution to each
// text part and base64-encodes them into a fabric.Definition. Unresolved
// references are intentionally discarded here — the dry-run surfaces them; the
// publish path leaves any unresolved (cosmetic) GUID as-is.
func buildDefinition(item LocalItem, env string, params Parameters, idMap map[string]string, resolver *Resolver, rb *Rebinder) (*fabric.Definition, error) {
	parts, _, err := SubstituteParts(item, env, params, idMap, resolver, rb)
	if err != nil {
		return nil, err
	}
	def := &fabric.Definition{}
	for _, part := range item.Parts { // preserve discovery order
		def.Parts = append(def.Parts, fabric.DefinitionPart{
			Path:        part.Path,
			Payload:     base64.StdEncoding.EncodeToString(parts[part.Path]),
			PayloadType: "InlineBase64",
		})
	}
	return def, nil
}

// reportDatasetRef parses a report's definition.pbir dataset reference once and
// classifies it three ways so the caller can act distinctly on each (instead of
// the old "" that conflated byConnection with no-reference and silently skipped
// both):
//   - byPath → refByPath with the model display name (e.g. "../MyModel.SemanticModel" → "MyModel").
//   - byConnection present → refByConnection (no resolvable local model name).
//   - neither / no pbir / unparseable → refNone (nothing to rebind).
func reportDatasetRef(report LocalItem) datasetRef {
	for _, part := range report.Parts {
		if path.Base(part.Path) != "definition.pbir" {
			continue
		}
		var pbir struct {
			DatasetReference struct {
				ByPath struct {
					Path string `json:"path"`
				} `json:"byPath"`
				ByConnection json.RawMessage `json:"byConnection"`
			} `json:"datasetReference"`
		}
		if err := json.Unmarshal(part.Content, &pbir); err != nil {
			return datasetRef{Kind: refNone}
		}
		if ref := pbir.DatasetReference.ByPath.Path; ref != "" {
			base := path.Base(ref) // "MyModel.SemanticModel"
			return datasetRef{Kind: refByPath, ModelName: strings.TrimSuffix(base, ".SemanticModel")}
		}
		if len(pbir.DatasetReference.ByConnection) > 0 && string(pbir.DatasetReference.ByConnection) != "null" {
			return datasetRef{Kind: refByConnection}
		}
		return datasetRef{Kind: refNone}
	}
	return datasetRef{Kind: refNone}
}
