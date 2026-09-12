package deploy

import (
	"strings"
	"unicode"
)

// TMDL accepts several spellings for the same model. Fabric stores a semantic
// model through the TMDL serializer, which always picks the canonical one; a
// hand-written .tmdl in git often uses another. Comparing the two as raw text
// then reports the model Changed on every deploy, forever, with nothing to fix.
// normalizeTMDL rewrites both sides into one spelling so only real differences
// survive. Two rules are canonicalized, both quoted from the TMDL language
// reference:
//
//   - Multi-line expressions may be wrapped in ``` or simply indented under
//     their declaration. "Using the three backticks delimiter is optional and
//     only required in unique situations" — the serializer emits it only when
//     the content would not survive a round trip (trailing whitespace, blank
//     lines carrying whitespace). Outer indentation is stripped on parse either
//     way, so only indentation RELATIVE to the expression's own left boundary
//     carries meaning.
//   - An object name is quoted only when it has to be: "The TMDL object name
//     must be enclosed in single quotes if it includes any of the following
//     characters: Dot, Equals, Colon, Single Quote, Whitespace." The serializer
//     drops optional quotes.
//
// Expression bodies are left byte-for-byte alone. Inside DAX and M a single
// quote delimits a table name, not a TMDL object name, so unquoting there
// would corrupt the comparison and hide real changes.
func normalizeTMDL(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		line := strings.TrimRight(unquoteTMDLName(lines[i]), " \t")
		i++
		// An opening fence sits alone at the end of the declaration line, so
		// dropping it leaves the line in its unfenced spelling.
		fenced := strings.HasSuffix(line, "```")
		if fenced {
			line = strings.TrimRight(strings.TrimSuffix(line, "```"), " \t")
		}
		out = append(out, line)
		if !strings.HasSuffix(line, "=") {
			continue
		}

		// A multi-line expression follows. Collect its body: up to the closing
		// fence, or — unfenced — while lines stay blank or indented at least as
		// deep as the body's own first line, since "the entire expression must
		// be within that indentation level".
		declWidth := len(leadingWhitespace(line))
		bodyWidth := -1
		var body []string
		for i < len(lines) {
			bl := lines[i]
			if fenced {
				if strings.TrimSpace(bl) == "```" {
					i++
					break
				}
			} else if strings.TrimSpace(bl) != "" {
				w := len(leadingWhitespace(bl))
				if bodyWidth < 0 {
					if w <= declWidth {
						break // not a body after all
					}
					bodyWidth = w
				} else if w < bodyWidth {
					break
				}
			}
			body = append(body, bl)
			i++
		}
		// Trailing blank lines belong after the expression, not inside it:
		// fenced they sit before the closing delimiter, unfenced after the last
		// body line, and both spellings must land in the same place.
		trailing := 0
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
			trailing++
		}
		out = append(out, reindentTMDLBody(body, leadingWhitespace(line))...)
		for ; trailing > 0; trailing-- {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

// reindentTMDLBody re-anchors an expression body two levels below its
// declaration, preserving relative indentation. Both spellings define the left
// boundary differently — unfenced by the parent's indentation, fenced by the
// closing delimiter — so pinning it to the declaration makes them converge
// while the body stays readable in the diff view.
func reindentTMDLBody(body []string, declIndent string) []string {
	min := -1
	for _, l := range body {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if w := len(leadingWhitespace(l)); min < 0 || w < min {
			min = w
		}
	}
	if min < 0 {
		return body
	}
	target := declIndent + "\t\t"
	out := make([]string, 0, len(body))
	for _, l := range body {
		if strings.TrimSpace(l) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, target+l[min:])
	}
	return out
}

// tmdlObjectTypes are the TOM object types a TMDL declaration can name. Only a
// line opening with one of these (optionally behind ref) has a name to unquote;
// everything else — property assignments, expression bodies, descriptions — is
// left alone.
var tmdlObjectTypes = map[string]bool{
	"model": true, "database": true, "table": true, "column": true,
	"measure": true, "partition": true, "hierarchy": true, "level": true,
	"relationship": true, "role": true, "perspective": true, "culture": true,
	"expression": true, "datasource": true, "annotation": true,
	"extendedproperty": true, "changedproperty": true, "calculationgroup": true,
	"calculationitem": true, "function": true, "querygroup": true,
	"variation": true, "perspectivetable": true, "perspectivecolumn": true,
	"perspectivemeasure": true, "perspectivehierarchy": true,
	"tablepermission": true, "columnpermission": true, "refreshpolicy": true,
	"formatstringdefinition": true, "detailrowsdefinition": true,
	"datacoveragedefinition": true, "linguisticmetadata": true, "set": true,
	"modelrolemember": true, "externalmodelrolemember": true,
	"namedexpression": true, "calculationexpression": true,
	"jsonextendedproperty": true, "stringextendedproperty": true,
}

// unquoteTMDLName drops the single quotes around a declaration's object name
// when the name does not require them. Lines that are not object declarations
// are returned unchanged.
func unquoteTMDLName(line string) string {
	indent := leadingWhitespace(line)
	rest := line[len(indent):]
	if rest == "" || strings.HasPrefix(rest, "///") {
		return line
	}
	prefix := indent
	tok, after := splitTMDLToken(rest)
	if strings.EqualFold(tok, "ref") {
		prefix += rest[:len(rest)-len(after)]
		rest = after
		tok, after = splitTMDLToken(rest)
	}
	if !tmdlObjectTypes[strings.ToLower(tok)] || !strings.HasPrefix(after, "'") {
		return line
	}
	prefix += rest[:len(rest)-len(after)]
	name, tail, ok := parseQuotedTMDLName(after)
	if !ok || !tmdlNameIsBare(name) {
		return line
	}
	return prefix + name + tail
}

// splitTMDLToken returns the first whitespace-delimited token of s and the
// remainder with the separating whitespace consumed.
func splitTMDLToken(s string) (string, string) {
	end := strings.IndexFunc(s, unicode.IsSpace)
	if end < 0 {
		return s, ""
	}
	rest := strings.TrimLeftFunc(s[end:], unicode.IsSpace)
	return s[:end], rest
}

// parseQuotedTMDLName reads a single-quoted name off the front of s, resolving
// the ” escape, and returns the name and whatever follows the closing quote.
func parseQuotedTMDLName(s string) (name, tail string, ok bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return b.String(), s[i+1:], true
	}
	return "", "", false
}

// tmdlNameIsBare reports whether a name can be written without quotes.
func tmdlNameIsBare(name string) bool {
	if name == "" {
		return false
	}
	return !strings.ContainsAny(name, ".=:'") &&
		strings.IndexFunc(name, unicode.IsSpace) < 0
}

func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}
