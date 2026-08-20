package deploy

import (
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// fabricOwnedFolder maps an item type to the folder inside its definition that
// Fabric itself writes and owns. It mirrors fabric-cicd's
// EXCLUDE_PATH_REGEX_MAPPING — `.pbi` for semantic models, reports and data
// agents, `.children` for eventhouses — which strips exactly these paths out of
// every publish payload.
//
// They cannot be treated as ordinary parts in either direction. Git often does
// not carry the folder at all while getDefinition returns it anyway: a semantic
// model's .pbi/editorSettings.json is written by Power BI Desktop, never by a
// deploy. Kept as a part, it reads as a part the publish would delete, on every
// single run — and that false positive lands on the part-removal gate, the last
// place one belongs.
var fabricOwnedFolder = map[string]string{
	"SemanticModel": ".pbi",
	"Report":        ".pbi",
	"DataAgent":     ".pbi",
	"Eventhouse":    ".children",
}

// IsFabricOwnedPart reports whether a definition part path falls inside the
// folder Fabric owns for that item type. Matched per path SEGMENT anywhere in
// the path, the same reach as fabric-cicd's `.*\.pbi[/\\].*`. Definition part
// paths always use forward slashes, on every platform.
func IsFabricOwnedPart(itemType, partPath string) bool {
	folder, ok := fabricOwnedFolder[itemType]
	if !ok {
		return false
	}
	for _, seg := range strings.Split(partPath, "/") {
		if seg == folder {
			return true
		}
	}
	return false
}

// DropFabricOwnedParts filters a DEPLOYED definition's parts through
// IsFabricOwnedPart. Discovery already drops them on the git side; both sides
// have to agree, or the compare reports every Fabric-written file as a removal.
// Returns parts unchanged when the type owns no such folder.
func DropFabricOwnedParts(itemType string, parts []fabric.DefinitionPart) []fabric.DefinitionPart {
	if _, ok := fabricOwnedFolder[itemType]; !ok {
		return parts
	}
	kept := make([]fabric.DefinitionPart, 0, len(parts))
	for _, p := range parts {
		if !IsFabricOwnedPart(itemType, p.Path) {
			kept = append(kept, p)
		}
	}
	return kept
}
