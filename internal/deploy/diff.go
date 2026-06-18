package deploy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// SubstituteParts applies logicalId + parameter.yml substitution, then (when rb
// is non-nil) auto-rebind of notebook lakehouse references, to each of an item's
// parts. Returns path -> substituted raw bytes (not base64), plus any
// references the rebinder could not resolve (tagged with the item name). Shared
// by the publish path (which base64-encodes the result) and the content-diff.
// A nil rb skips rebinding entirely.
func SubstituteParts(item LocalItem, env string, params Parameters, idMap map[string]string, resolver *Resolver, rb *Rebinder) (map[string][]byte, []UnresolvedRef, error) {
	out := make(map[string][]byte, len(item.Parts))
	var unresolved []UnresolvedRef
	for _, part := range item.Parts {
		content := ReplaceLogicalIds(part.Content, idMap)
		substituted, err := params.ApplyFindReplace(env, item, part.Path, content, resolver.Resolve)
		if err != nil {
			return nil, nil, fmt.Errorf("part %s: %w", part.Path, err)
		}
		if rb != nil && strings.HasPrefix(path.Base(part.Path), "notebook-content.") {
			rebound, outcome := rb.RebindNotebookLakehouses(substituted)
			substituted = rebound
			for i := range outcome.Unresolved {
				outcome.Unresolved[i].ItemName = item.DisplayName
			}
			unresolved = append(unresolved, outcome.Unresolved...)
		}
		out[part.Path] = substituted
	}
	return out, unresolved, nil
}

// normalizePart canonicalizes a part's bytes so cosmetic differences Fabric
// introduces when it stores/returns a definition don't read as real changes.
// Valid JSON is re-marshalled with sorted keys; everything else is treated as
// text (CRLF→LF, trailing per-line whitespace stripped, surrounding blank lines
// trimmed). Best-effort — per-type normalizers can refine this later.
func normalizePart(content []byte) []byte {
	var v any
	if json.Unmarshal(content, &v) == nil {
		if canon, err := json.Marshal(v); err == nil {
			return canon
		}
	}
	s := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return []byte(strings.TrimSpace(strings.Join(lines, "\n")))
}

// PartsChanged reports whether the local substituted parts differ from the
// deployed definition, after per-part normalization. A differing set of part
// paths (one added or removed) counts as changed.
func PartsChanged(localParts map[string][]byte, deployed *fabric.Definition) bool {
	deployedNorm := make(map[string][]byte, len(deployed.Parts))
	for _, p := range deployed.Parts {
		raw, err := base64.StdEncoding.DecodeString(p.Payload)
		if err != nil {
			raw = []byte(p.Payload) // non-base64 payloads compared as-is
		}
		deployedNorm[p.Path] = normalizePart(raw)
	}
	if len(deployedNorm) != len(localParts) {
		return true
	}
	for path, lb := range localParts {
		db, ok := deployedNorm[path]
		if !ok {
			return true
		}
		if !bytes.Equal(normalizePart(lb), db) {
			return true
		}
	}
	return false
}
