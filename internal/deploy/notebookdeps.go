package deploy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// notebookSourcePart reports whether p is the .py source-format content part of
// a notebook. Only .py: the .sql and .scala forms mark their metadata with
// other comment prefixes, which metaSection does not read.
func notebookSourcePart(p string) bool {
	return path.Base(p) == "notebook-content.py"
}

// decodeJSON decodes exactly one JSON value from s, keeping numbers exact.
// Anything after the first value is an error, so trailing content can never
// vanish from a comparison.
func decodeJSON(s string, v any) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errTrailingJSON
	}
	return nil
}

var errTrailingJSON = errors.New("trailing content after JSON value")

// rewriteMeta re-emits the notebook-level metadata as sorted, two-space
// indented "# META" lines, first setting "dependencies" when deps is non-nil.
// Returns content unchanged and false when there is no parseable section.
func rewriteMeta(content string, deps map[string]any) (string, bool) {
	lines := strings.Split(content, "\n")
	start, end, meta := metaSection(lines)
	if len(meta) == 0 {
		return content, false
	}
	var doc map[string]any
	if decodeJSON(strings.Join(meta, "\n"), &doc) != nil || doc == nil {
		return content, false
	}
	if deps != nil {
		doc["dependencies"] = deps
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(doc) != nil {
		return content, false
	}
	res := make([]string, 0, len(lines))
	res = append(res, lines[:start]...)
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		res = append(res, strings.TrimRight("# META "+l, " "))
	}
	res = append(res, lines[end+1:]...)
	return strings.Join(res, "\n"), true
}

// sameNotebookMeta reports whether two normalized versions of a notebook
// source part differ only in the layout of their notebook-level metadata (key
// order, indentation). The diff view keeps the original text; this only
// decides whether there is a difference at all.
func sameNotebookMeta(partPath, a, b string) bool {
	if !notebookSourcePart(partPath) {
		return false
	}
	ca, okA := rewriteMeta(a, nil)
	cb, okB := rewriteMeta(b, nil)
	return okA && okB && ca == cb
}

// metaDependencies returns the "dependencies" object of a .py notebook's
// notebook-level metadata, or nil when there is none or it is empty.
func metaDependencies(content []byte) map[string]any {
	raw, ok := extractMetadataJSON(content)
	if !ok {
		return nil
	}
	var doc struct {
		Dependencies map[string]any `json:"dependencies"`
	}
	if decodeJSON(raw, &doc) != nil || len(doc.Dependencies) == 0 {
		return nil
	}
	return doc.Dependencies
}

// ipynbDependencies returns metadata.dependencies from an ipynb document, or nil
// when it has none.
func ipynbDependencies(nb []byte) map[string]any {
	var doc struct {
		Metadata struct {
			Dependencies map[string]any `json:"dependencies"`
		} `json:"metadata"`
	}
	if decodeJSON(string(nb), &doc) != nil || len(doc.Metadata.Dependencies) == 0 {
		return nil
	}
	return doc.Metadata.Dependencies
}

// RestoreNotebookDependencies repairs a deployed notebook definition fetched in
// the default (.py) format. Fabric's FabricGitSource export can leave out the
// notebook-level "dependencies" block (lakehouse, environment) that its ipynb
// export of the same item still carries; compared as text, every bound notebook
// would then read as Changed on every deploy, forever.
//
// For each deployed .py part with no dependencies while the local part declares
// some, the block is copied from the ipynb export. fetchIpynb runs at most once,
// and only when such a part exists. When the ipynb export has none either, the
// part is left alone: the target really is unbound and the diff is real.
func RestoreNotebookDependencies(local LocalItem, deployed *fabric.Definition, fetchIpynb func() ([]byte, error)) error {
	if deployed == nil || local.Type != "Notebook" || DefinitionFormat(local) != "" {
		return nil
	}
	localByPath := make(map[string][]byte, len(local.Parts))
	for _, p := range local.Parts {
		localByPath[p.Path] = p.Content
	}
	var deps map[string]any
	fetched := false
	for i, p := range deployed.Parts {
		if !notebookSourcePart(p.Path) || metaDependencies(localByPath[p.Path]) == nil {
			continue
		}
		raw := p.Decoded()
		if metaDependencies(raw) != nil {
			continue
		}
		if !fetched {
			fetched = true
			nb, err := fetchIpynb()
			if err != nil {
				return err
			}
			deps = ipynbDependencies(nb)
		}
		if deps == nil {
			return nil
		}
		if patched, ok := rewriteMeta(string(raw), deps); ok {
			deployed.Parts[i].Payload = base64.StdEncoding.EncodeToString([]byte(patched))
			deployed.Parts[i].PayloadType = "InlineBase64"
		}
	}
	return nil
}
