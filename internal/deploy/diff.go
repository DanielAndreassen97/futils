package deploy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// SubstituteParts applies logicalId substitution, then (when rb is non-nil)
// auto-rebind of notebook lakehouse references, to each of an item's parts.
// Returns path -> substituted raw bytes (not base64), plus a RebindOutcome with
// any changes applied and references the rebinder could not resolve (tagged with
// the item name). Shared by the publish path (which base64-encodes the result)
// and the content-diff. A nil rb skips rebinding entirely.
func SubstituteParts(item LocalItem, idMap map[string]string, resolver *Resolver, rb *Rebinder) (map[string][]byte, RebindOutcome, error) {
	out := make(map[string][]byte, len(item.Parts))
	var outcome RebindOutcome
	for _, part := range item.Parts {
		substituted := ReplaceLogicalIds(part.Content, idMap)
		if rb != nil {
			subbed, subOutcome := rb.ApplyCustomSubstitutions(item, part.Path, substituted)
			substituted = subbed
			outcome.Changes = append(outcome.Changes, subOutcome.Changes...)
			for _, u := range subOutcome.Unresolved {
				u.ItemName = item.DisplayName
				// Re-dedup across parts: the same broken ref usually repeats in
				// every part/table expression of the item.
				outcome.AddUnresolved(u)
			}

			rebound, partOutcome := rb.RebindPart(item, part.Path, substituted)
			substituted = rebound
			outcome.Changes = append(outcome.Changes, partOutcome.Changes...)
			for _, u := range partOutcome.Unresolved {
				u.ItemName = item.DisplayName
				outcome.AddUnresolved(u)
			}
		}
		out[part.Path] = substituted
	}
	return out, outcome, nil
}

// normalizePart canonicalizes a part's bytes so cosmetic differences Fabric
// introduces when it stores/returns a definition don't read as real changes.
// Valid JSON is re-marshalled with sorted keys; numbers are decoded as
// json.Number (not float64) so integers above 2^53 — e.g. numeric IDs or
// epoch-nanosecond timestamps — keep full precision instead of two distinct
// values collapsing to the same float. Everything else is treated as text
// (CRLF→LF, trailing per-line whitespace stripped, surrounding blank lines
// trimmed). Best-effort — per-type normalizers can refine this later.
func normalizePart(content []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) == nil && !dec.More() {
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

// PartDiff is the normalized old (deployed) vs new (substituted-local) text of
// one item part that differs. Old is empty when the part is new locally; New is
// empty when the part exists only in the deployed definition.
type PartDiff struct {
	Path string
	Old  string
	New  string
}

// DiffParts returns, for each part whose normalized content differs between the
// substituted local parts and the deployed definition, the normalized old
// (deployed) and new (local) text. It is the single source of the content
// verdict (len == 0 means unchanged).
func DiffParts(localParts map[string][]byte, deployed *fabric.Definition) []PartDiff {
	deployedNorm := make(map[string]string, len(deployed.Parts))
	for _, p := range deployed.Parts {
		if path.Base(p.Path) == ".platform" {
			continue // local parts exclude .platform; its description is diffed as a field
		}
		raw, err := base64.StdEncoding.DecodeString(p.Payload)
		if err != nil {
			raw = []byte(p.Payload)
		}
		deployedNorm[p.Path] = string(normalizePart(raw))
	}
	var diffs []PartDiff
	seen := make(map[string]bool, len(localParts))
	for path, lb := range localParts {
		seen[path] = true
		newN := string(normalizePart(lb))
		oldN := deployedNorm[path]
		if newN != oldN {
			diffs = append(diffs, PartDiff{Path: path, Old: oldN, New: newN})
		}
	}
	for path, oldN := range deployedNorm {
		if !seen[path] {
			diffs = append(diffs, PartDiff{Path: path, Old: oldN, New: ""})
		}
	}
	return diffs
}

// DeployedDescription returns the item description stored in the deployed
// definition's .platform part (empty if there is none or it can't be parsed).
// futils excludes .platform from the part-by-part diff, so description drift is
// surfaced separately as a field-level change.
func DeployedDescription(deployed *fabric.Definition) string {
	for _, p := range deployed.Parts {
		if path.Base(p.Path) != ".platform" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(p.Payload)
		if err != nil {
			raw = []byte(p.Payload)
		}
		meta, err := parsePlatform(raw)
		if err != nil {
			return ""
		}
		return meta.Description
	}
	return ""
}
