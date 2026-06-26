package deploy

import "encoding/json"

// allowPairingByName makes the bulk import pair an item against an existing
// workspace item by display name + type rather than by logicalId. Combined with
// stripLogicalID, this matches the per-item backend's name-based identity model,
// so bulk Updates land on items previously deployed by either backend. Flip to
// false (and stop stripping logicalId) only if testing shows Fabric needs strict
// logicalId pairing.
const allowPairingByName = true

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
