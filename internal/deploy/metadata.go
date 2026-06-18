package deploy

import (
	"encoding/json"
	"strings"
)

// lakehouseBlock mirrors a Fabric notebook's dependencies.lakehouse metadata.
type lakehouseBlock struct {
	DefaultLakehouse            string           `json:"default_lakehouse"`
	DefaultLakehouseName        string           `json:"default_lakehouse_name"`
	DefaultLakehouseWorkspaceID string           `json:"default_lakehouse_workspace_id"`
	KnownLakehouses             []knownLakehouse `json:"known_lakehouses"`
}

type knownLakehouse struct {
	ID string `json:"id"`
}

// extractMetadataJSON returns the JSON text of a Fabric notebook's metadata.
// Two on-disk shapes exist: a .ipynb (the whole file is JSON), or the Fabric
// .py source format where the trailing "# METADATA" section holds "# META"-
// prefixed JSON lines. Returns ("", false) when no metadata is found. Only the
// first "# METADATA" section is read — Fabric emits exactly one notebook-level
// section (the cell sections use "# CELL"/"# MARKDOWN" markers, not "# METADATA").
func extractMetadataJSON(content []byte) (string, bool) {
	if json.Valid(content) {
		return string(content), true
	}
	var meta []string
	inMeta := false
	for _, ln := range strings.Split(string(content), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "# METADATA *") {
			inMeta = true
			continue
		}
		if !inMeta {
			continue
		}
		if t == "# META" {
			meta = append(meta, "")
			continue
		}
		if strings.HasPrefix(t, "# META ") {
			meta = append(meta, strings.TrimPrefix(t, "# META "))
			continue
		}
		if t == "" {
			continue
		}
		break // a non-META line ends the metadata section
	}
	if len(meta) == 0 {
		return "", false
	}
	return strings.Join(meta, "\n"), true
}

// parseNotebookLakehouse extracts the lakehouse dependency block from a notebook
// part, handling both the .ipynb (metadata.dependencies.lakehouse) and .py
// (dependencies.lakehouse) shapes. Returns a zero block and false when the
// notebook declares no lakehouse dependency — absence is not an error.
func parseNotebookLakehouse(content []byte) (lakehouseBlock, bool) {
	raw, ok := extractMetadataJSON(content)
	if !ok {
		return lakehouseBlock{}, false
	}
	var doc struct {
		Dependencies struct {
			Lakehouse lakehouseBlock `json:"lakehouse"`
		} `json:"dependencies"`
		Metadata struct {
			Dependencies struct {
				Lakehouse lakehouseBlock `json:"lakehouse"`
			} `json:"dependencies"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return lakehouseBlock{}, false
	}
	lh := doc.Metadata.Dependencies.Lakehouse // .ipynb shape
	if lh.DefaultLakehouse == "" && len(lh.KnownLakehouses) == 0 {
		lh = doc.Dependencies.Lakehouse // .py METADATA-block shape
	}
	if lh.DefaultLakehouse == "" && len(lh.KnownLakehouses) == 0 {
		return lakehouseBlock{}, false
	}
	return lh, true
}
