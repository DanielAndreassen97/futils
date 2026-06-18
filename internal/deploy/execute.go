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
	Name   string
	Type   string
	Action Action
	ID     string // deployed item ID
	Err    error
}

// Execute publishes a plan against the target workspace. For each item, in
// order, it applies logicalId substitution and parameter.yml find_replace to
// every part; when rb is non-nil, it also auto-rebinds notebook lakehouse
// references by name. It then encodes parts to base64 and creates or updates
// the item. It records each item's deployed GUID (keyed by logicalId and by
// type+name) so later items in the run can reference them; reports are rebound
// to any semantic model published earlier in the same run.
//
// A per-item error is captured in its Result (the run continues); Execute only
// returns a top-level error for a setup failure that aborts everything.
func Execute(client FabricClient, token string, target fabric.Workspace, env string, plan []PlannedItem, params Parameters, rb *Rebinder) ([]Result, error) {
	resolver := NewResolver(client, token, target)
	idMap := map[string]string{}         // logicalId -> deployed GUID
	modelIDByName := map[string]string{} // SemanticModel displayName -> deployed GUID
	results := make([]Result, 0, len(plan))

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

		if p.Item.LogicalID != "" {
			idMap[p.Item.LogicalID] = deployedID
		}
		if p.Item.Type == "SemanticModel" {
			modelIDByName[p.Item.DisplayName] = deployedID
		}
		if p.Item.Type == "Report" {
			if modelName := reportModelFolderName(p.Item); modelName != "" {
				if datasetID, ok := modelIDByName[modelName]; ok {
					if err := client.RebindReport(token, target.ID, deployedID, datasetID); err != nil {
						res.Err = fmt.Errorf("created but rebind failed: %w", err)
					}
				}
			}
		}
		results = append(results, res)
	}
	return results, nil
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

// reportModelFolderName extracts the semantic model's display name from a
// report's definition.pbir byPath reference (e.g. "../MyModel.SemanticModel"
// -> "MyModel"). Returns "" if the report uses byConnection or has no pbir.
func reportModelFolderName(report LocalItem) string {
	for _, part := range report.Parts {
		if path.Base(part.Path) != "definition.pbir" {
			continue
		}
		var pbir struct {
			DatasetReference struct {
				ByPath struct {
					Path string `json:"path"`
				} `json:"byPath"`
			} `json:"datasetReference"`
		}
		if err := json.Unmarshal(part.Content, &pbir); err != nil {
			return ""
		}
		ref := pbir.DatasetReference.ByPath.Path
		if ref == "" {
			return ""
		}
		base := path.Base(ref) // "MyModel.SemanticModel"
		return strings.TrimSuffix(base, ".SemanticModel")
	}
	return ""
}
