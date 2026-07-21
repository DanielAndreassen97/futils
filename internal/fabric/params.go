// Package fabric provides helpers for interacting with Microsoft Fabric
// notebook items: parsing their definitions and triggering RunNotebook jobs.
package fabric

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Parameter describes a discovered notebook parameter. Default holds the
// literal parsed from the notebook's parameters cell. Type is one of the
// four lowercase Python-style names that Fabric's RunNotebook job actually
// accepts on the wire: "string", "bool", "int", "float". (Microsoft Learn
// documents PascalCase Text/Boolean/Integer/Number for the generic
// release API — but Microsoft's own fabric-cli uses these lowercase names
// against the Core endpoint, and that's what works in practice.)
type Parameter struct {
	Name    string
	Type    string
	Default any
	// RawDefault is the literal text from the notebook source (e.g. `"foo"`,
	// `False`, `42`). Kept for display in the TUI so the user sees exactly
	// what was declared.
	RawDefault string
}

// Fabric RunNotebook parameter types. These match the lowercase Python-style
// names that fabric-cli uses (see fab_types.py in microsoft/fabric-cli) and
// that the user's original API example demonstrated.
const (
	TypeString = "string"
	TypeBool   = "bool"
	TypeInt    = "int"
	TypeFloat  = "float"
)

// ipynb is a minimal shape for the fields we care about. The real Jupyter
// schema is much larger, but we only read cells/metadata/source.
type ipynb struct {
	Cells []ipynbCell `json:"cells"`
}

type ipynbCell struct {
	CellType string          `json:"cell_type"`
	Metadata ipynbCellMeta   `json:"metadata"`
	Source   json.RawMessage `json:"source"`
}

type ipynbCellMeta struct {
	Tags []string `json:"tags"`
}

// ParseParameters reads a Fabric notebook's .ipynb content and extracts the
// parameters declared in the cell tagged "parameters" (Papermill convention).
//
// Returns an empty slice (not an error) if no parameters cell exists or the
// cell contains no recognisable declarations — callers decide whether that
// means "no parameters" or "prompt the user for free-form input".
func ParseParameters(content []byte) ([]Parameter, error) {
	var nb ipynb
	if err := json.Unmarshal(content, &nb); err != nil {
		return nil, fmt.Errorf("parse notebook: %w", err)
	}

	source, ok := findParametersSource(nb)
	if !ok {
		return []Parameter{}, nil
	}

	return parseAssignments(source), nil
}

// findParametersSource returns the concatenated source of the first cell
// tagged "parameters", or false if no such cell exists.
func findParametersSource(nb ipynb) (string, bool) {
	for _, cell := range nb.Cells {
		if cell.CellType != "code" {
			continue
		}
		if !hasTag(cell.Metadata.Tags, "parameters") {
			continue
		}
		src, err := decodeSource(cell.Source)
		if err != nil {
			continue
		}
		return src, true
	}
	return "", false
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// decodeSource handles the ipynb quirk where "source" is either a single
// string or an array of strings (with embedded newlines).
func decodeSource(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	// Try string first — that's the common Fabric export.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return "", err
	}
	return strings.Join(lines, ""), nil
}

// parseAssignments walks the parameters cell line-by-line, picking up simple
// `name = literal` assignments. Lines it can't interpret (comments, complex
// expressions, multi-line strings) are silently skipped.
func parseAssignments(source string) []Parameter {
	var out []Parameter
	for _, line := range strings.Split(source, "\n") {
		p, ok := parseAssignment(line)
		if ok {
			out = append(out, p)
		}
	}
	return out
}

// parseAssignment parses a single line of the form `name = literal` (with
// optional inline comment). Returns ok=false for anything that isn't a
// recognisable simple assignment.
func parseAssignment(line string) (Parameter, bool) {
	// Strip leading/trailing whitespace. Skip comments and blanks.
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return Parameter{}, false
	}

	// Find the first '=' that isn't part of ==, !=, <=, >=, := etc.
	eqIdx := findAssignmentEquals(trimmed)
	if eqIdx < 0 {
		return Parameter{}, false
	}

	name := strings.TrimSpace(trimmed[:eqIdx])
	rhs := strings.TrimSpace(trimmed[eqIdx+1:])

	// Strip type annotation (`name: int = 5` → `name`).
	if colon := strings.IndexByte(name, ':'); colon >= 0 {
		name = strings.TrimSpace(name[:colon])
	}

	if !isValidIdent(name) {
		return Parameter{}, false
	}

	// Strip trailing inline comment, respecting string boundaries.
	rhs = stripInlineComment(rhs)
	rhs = strings.TrimSpace(rhs)
	if rhs == "" {
		return Parameter{}, false
	}

	typ, val, ok := parsePythonLiteral(rhs)
	if !ok {
		return Parameter{}, false
	}

	return Parameter{
		Name:       name,
		Type:       typ,
		Default:    val,
		RawDefault: rhs,
	}, true
}

// findAssignmentEquals returns the index of the first single `=` that marks
// an assignment, or -1 if none. Skips `==`, `!=`, `<=`, `>=`, and equals
// signs inside string literals.
func findAssignmentEquals(s string) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && (inSingle || inDouble):
			i++ // skip escaped char
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '=' && !inSingle && !inDouble:
			// Check for != <= >= == and walrus :=
			if i > 0 {
				prev := s[i-1]
				if prev == '!' || prev == '<' || prev == '>' || prev == '=' || prev == ':' {
					continue
				}
			}
			if i+1 < len(s) && s[i+1] == '=' {
				i++ // skip the second = of ==
				continue
			}
			return i
		}
	}
	return -1
}

// isValidIdent returns true if s is a plausible Python identifier. We don't
// accept everything Python does (unicode, etc.) — just ASCII letters, digits
// and underscores, not starting with a digit.
func isValidIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_':
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// stripInlineComment removes a trailing `# ...` comment while respecting
// string literals (a `#` inside quotes is part of the value).
func stripInlineComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && (inSingle || inDouble):
			i++
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '#' && !inSingle && !inDouble:
			return s[:i]
		}
	}
	return s
}

// parsePythonLiteral recognises the four literal shapes Fabric's RunNotebook
// API accepts. Anything else (None, lists, dicts, expressions, f-strings)
// returns ok=false so the caller can skip it.
func parsePythonLiteral(raw string) (typ string, val any, ok bool) {
	switch raw {
	case "True":
		return TypeBool, true, true
	case "False":
		return TypeBool, false, true
	case "None":
		return "", nil, false
	}

	// String literal — single or double quoted. Reject triple-quoted and
	// f/r/b prefixes (too messy to handle well; fall back to free-form).
	if len(raw) >= 2 {
		first, last := raw[0], raw[len(raw)-1]
		if (first == '"' || first == '\'') && first == last {
			if strings.HasPrefix(raw, `"""`) || strings.HasPrefix(raw, `'''`) {
				return "", nil, false
			}
			unquoted, err := unquotePython(raw)
			if err != nil {
				return "", nil, false
			}
			return TypeString, unquoted, true
		}
	}

	// Numeric: try int first, then float. Strip a leading sign.
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return TypeInt, i, true
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return TypeFloat, f, true
	}

	return "", nil, false
}

// LakehouseBinding mirrors the default-lakehouse fields a Fabric notebook
// stores under metadata.dependencies.lakehouse. Git-committed notebooks
// frequently ship with Name and WorkspaceID empty while LakehouseID still
// holds a GUID — the workspace id is environment-specific, so authoring or
// deployment tooling leaves it blank. That half-filled state runs fine
// interactively (the portal backfills the current workspace) but fails a
// headless job submit at session attach: "LakehouseWorkspaceId is not a
// valid GUID".
type LakehouseBinding struct {
	// LakehouseID is metadata.default_lakehouse — the lakehouse item GUID,
	// or "" when the notebook pins no default lakehouse at all.
	LakehouseID string
	Name        string
	// WorkspaceID is the lakehouse's home workspace. Empty is the broken
	// state we detect and repair.
	WorkspaceID string
}

// NeedsWorkspaceResolution reports the specific broken-binding pattern worth
// repairing: a lakehouse is pinned but its workspace id is missing. A
// complete binding (both set) and a notebook with no lakehouse at all (both
// empty) both return false — callers should leave those untouched.
func (b LakehouseBinding) NeedsWorkspaceResolution() bool {
	return b.LakehouseID != "" && b.WorkspaceID == ""
}

// ParseLakehouseBinding extracts the default-lakehouse binding from a Fabric
// notebook's .ipynb metadata. Returns a zero binding (not an error) when the
// notebook declares no lakehouse dependency — same "absence is not failure"
// contract as ParseParameters.
func ParseLakehouseBinding(content []byte) (LakehouseBinding, error) {
	var nb struct {
		Metadata struct {
			Dependencies struct {
				Lakehouse struct {
					DefaultLakehouse            string `json:"default_lakehouse"`
					DefaultLakehouseName        string `json:"default_lakehouse_name"`
					DefaultLakehouseWorkspaceID string `json:"default_lakehouse_workspace_id"`
				} `json:"lakehouse"`
			} `json:"dependencies"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(content, &nb); err != nil {
		return LakehouseBinding{}, fmt.Errorf("parse notebook metadata: %w", err)
	}
	lh := nb.Metadata.Dependencies.Lakehouse
	return LakehouseBinding{
		LakehouseID: lh.DefaultLakehouse,
		Name:        lh.DefaultLakehouseName,
		WorkspaceID: lh.DefaultLakehouseWorkspaceID,
	}, nil
}

// unquotePython handles the common subset of Python string literals: single
// or double quotes with the usual backslash escapes. It isn't a full Python
// lexer — \x, \u, \N{} and similar aren't supported because notebook
// parameter defaults almost never use them.
func unquotePython(raw string) (string, error) {
	if len(raw) < 2 {
		return "", fmt.Errorf("not a string literal: %q", raw)
	}
	quote := raw[0]
	if (quote != '"' && quote != '\'') || raw[len(raw)-1] != quote {
		return "", fmt.Errorf("not a string literal: %q", raw)
	}
	inner := raw[1 : len(raw)-1]

	var b strings.Builder
	b.Grow(len(inner))
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(inner) {
			return "", fmt.Errorf("trailing backslash in %q", raw)
		}
		next := inner[i+1]
		switch next {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '0':
			b.WriteByte(0)
		case '\\', '\'', '"':
			b.WriteByte(next)
		default:
			// Python keeps unknown escapes literally, e.g. `"\d"` stays `\d`.
			b.WriteByte('\\')
			b.WriteByte(next)
		}
		i++
	}
	return b.String(), nil
}

// pipelineParamSpec is one entry in a data pipeline's
// properties.parameters block. type is Fabric's pipeline-parameter type
// ("string", "int", "float", "bool", "array", "object"); defaultValue is the
// declared default (may be absent).
type pipelineParamSpec struct {
	Type         string          `json:"type"`
	DefaultValue json.RawMessage `json:"defaultValue"`
}

// ParsePipelineParameters reads a data pipeline's parameters and their
// declared defaults from a pipeline-content.json definition. The result is
// sorted by name and reuses the Parameter shape (and Type* names) so the same
// ParameterForm renders it — array/object params surface as string (their JSON
// text), since the form edits them as raw text. Returns an empty slice when the
// pipeline declares no parameters.
func ParsePipelineParameters(content []byte) ([]Parameter, error) {
	var doc struct {
		Properties struct {
			Parameters map[string]pipelineParamSpec `json:"parameters"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("parse pipeline definition: %w", err)
	}
	names := make([]string, 0, len(doc.Properties.Parameters))
	for name := range doc.Properties.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Parameter, 0, len(names))
	for _, name := range names {
		spec := doc.Properties.Parameters[name]
		p := Parameter{Name: name, Type: pipelineParamType(spec.Type)}
		if len(spec.DefaultValue) > 0 {
			p.Default, p.RawDefault = pipelineDefault(p.Type, spec.DefaultValue)
		}
		out = append(out, p)
	}
	return out, nil
}

// pipelineParamType maps a pipeline parameter's declared type to the Type*
// the form understands. array/object have no dedicated field, so they're
// edited as raw JSON text (string).
func pipelineParamType(t string) string {
	switch strings.ToLower(t) {
	case "bool":
		return TypeBool
	case "int":
		return TypeInt
	case "float":
		return TypeFloat
	default: // string, array, object, secureString, and any unknown type
		return TypeString
	}
}

// pipelineDefault decodes a parameter's raw JSON defaultValue into the typed
// Default the form pre-fills and the RawDefault text it shows. Bool/int/float
// decode to their Go type (matching collectOverrides' default-comparison);
// everything else is kept as its source text.
func pipelineDefault(typ string, raw json.RawMessage) (any, string) {
	switch typ {
	case TypeBool:
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b, strconv.FormatBool(b)
		}
	case TypeInt:
		var n int64
		if json.Unmarshal(raw, &n) == nil {
			return n, strconv.FormatInt(n, 10)
		}
	case TypeFloat:
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			return f, strconv.FormatFloat(f, 'g', -1, 64)
		}
	}
	// string / array / object / a bool-or-number that didn't fit: show the
	// underlying text, unquoting a plain JSON string so the form seeds the
	// value itself rather than a quoted literal.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, s
	}
	return string(raw), string(raw)
}
