package deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// allowPairingByName makes the bulk import pair an item against an existing
// workspace item by display name + type rather than by logicalId. Combined with
// stripLogicalID, this matches the per-item backend's name-based identity model,
// so bulk Updates land on items previously deployed by either backend. Flip to
// false (and stop stripping logicalId) only if testing shows Fabric needs strict
// logicalId pairing.
const allowPairingByName = true

// BulkImport publishes every item in items into target in ONE bulkImportDefinitions
// call. Callers pass items already grouped by target workspace (the API is
// workspace-scoped). For each item it runs SubstituteParts (logicalId is a no-op
// here — idMap is empty; rb applies cross-env rebind of notebook/semantic-model
// references and custom substitutions, exactly as the per-item backend does),
// base64-encodes each part under its workspace-absolute path, and appends the
// item's .platform (with logicalId stripped, so pairing is by name+type).
//
// Report→model byPath references and overall dependency order are resolved by
// Fabric within the payload, so the per-item BuildPlan/publishOrder and the
// post-deploy RebindReports byPath pass are NOT needed here. (resolver is passed
// nil: SubstituteParts does not use it.)
func BulkImport(client FabricClient, token string, target fabric.Workspace, items []LocalItem, rb *Rebinder) ([]Result, error) {
	var parts []fabric.DefinitionPart
	for _, item := range items {
		subbed, _, err := SubstituteParts(item, map[string]string{}, nil, rb)
		if err != nil {
			return nil, fmt.Errorf("prepare %s %q: %w", item.Type, item.DisplayName, err)
		}
		for _, p := range item.Parts { // preserve discovery order
			parts = append(parts, fabric.DefinitionPart{
				Path:        "/" + item.FolderPath + "/" + p.Path,
				Payload:     base64.StdEncoding.EncodeToString(subbed[p.Path]),
				PayloadType: "InlineBase64",
			})
		}
		parts = append(parts, fabric.DefinitionPart{
			Path:        "/" + item.FolderPath + "/.platform",
			Payload:     base64.StdEncoding.EncodeToString(stripLogicalID(item.Platform)),
			PayloadType: "InlineBase64",
		})
	}

	res, err := client.BulkImportDefinitions(token, target.ID, parts, fabric.BulkImportOptions{AllowPairingByName: allowPairingByName})
	if err != nil {
		return nil, err
	}

	out := bulkResultsToResults(res.Details)

	// The report is built from the API's returned details, so an item the beta API
	// silently omits would vanish from the report/history. Flag any sent item with
	// no matching returned result (by the same type+name identity the deploy uses)
	// as an error rather than dropping it silently.
	returned := make(map[string]bool, len(out))
	for _, r := range out {
		returned[key(r.Type, r.Name)] = true
	}
	for _, item := range items {
		if !returned[key(item.Type, item.DisplayName)] {
			out = append(out, Result{
				Name: item.DisplayName,
				Type: item.Type,
				Err:  fmt.Errorf("bulk import returned no result for %s %q — verify it in the target workspace", item.Type, item.DisplayName),
			})
		}
	}

	return out, nil
}

// joinWarning concatenates two non-empty warning fragments with "; ".
func joinWarning(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "; " + addition
}

// actionFromOpType maps the API's operationType to a deploy Action.
func actionFromOpType(op string) Action {
	if op == "Update" {
		return ActionUpdate
	}
	return ActionCreate
}

// bulkResultsToResults maps per-item bulk details to deploy Results.
// SucceededDespiteFailures is a non-fatal Warning (the item was published but the
// API reported partial issues); anything other than Succeeded/SucceededDespiteFailures
// is a per-item Err. Empty Err+Warning means a clean success.
func bulkResultsToResults(details []fabric.BulkImportDetail) []Result {
	out := make([]Result, 0, len(details))
	for _, d := range details {
		r := Result{Name: d.ItemDisplayName, Type: d.ItemType, ID: d.ItemID, Action: actionFromOpType(d.OperationType)}
		switch d.OperationStatus {
		case "Succeeded":
			// clean
		case "SucceededDespiteFailures":
			r.Warning = "imported with partial failures (SucceededDespiteFailures) — verify the item in the target workspace"
		default: // Failed or any future/unknown status
			r.Err = fmt.Errorf("bulk import reported operationStatus=%q for %s %q", d.OperationStatus, d.ItemType, d.ItemDisplayName)
		}
		out = append(out, r)
	}
	return out
}

// stripLogicalID removes config.logicalId from a raw .platform payload so the
// bulk import pairs the item by name+type (see allowPairingByName). Other fields
// (metadata, config.version, $schema) are preserved. Best-effort: unparseable
// input, or input with no config.logicalId, is returned unchanged.
func stripLogicalID(platform []byte) []byte {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(platform, &top); err != nil {
		return platform
	}
	cfgRaw, ok := top["config"]
	if !ok {
		return platform
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		return platform
	}
	if _, has := cfg["logicalId"]; !has {
		return platform
	}
	delete(cfg, "logicalId")
	newCfg, err := json.Marshal(cfg)
	if err != nil {
		return platform
	}
	top["config"] = newCfg
	out, err := json.Marshal(top)
	if err != nil {
		return platform
	}
	return out
}
