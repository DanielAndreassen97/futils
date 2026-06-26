// cmd/schemacompare_report.go
package cmd

import (
	"fmt"
	"html"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// renderSchemaCompareReport builds a self-contained HTML report, reusing the
// deploy report's <style> block. One collapsible <details> per lakehouse, with
// its differing tables and per-column changes. All content is HTML-escaped.
func renderSchemaCompareReport(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>futils schema compare</title>`)
	b.WriteString(deployReportStyle)
	b.WriteString(`</head><body>`)
	b.WriteString(`<div class="hero"><h1>futils schema compare</h1>`)
	fmt.Fprintf(&b, `<div class="sub"><b>%s</b> → <b>%s</b> · "+" only in source · "-" only in target · "~" type changed</div></div>`,
		html.EscapeString(srcLabel), html.EscapeString(tgtLabel))

	for _, lh := range diffs {
		b.WriteString(`<details class="item changed" open><summary><span class="dot changed"></span>`)
		b.WriteString(html.EscapeString(lh.Lakehouse))
		fmt.Fprintf(&b, ` <span class="t">%s · %d matching</span><span class="chev">▾</span></summary>`,
			html.EscapeString(strings.Join(lh.Schemas, ", ")), lh.Matching)
		if len(lh.Tables) == 0 {
			b.WriteString(`<div class="empty">No differences.</div>`)
		}
		for _, td := range lh.Tables {
			name := html.EscapeString(schemacompare.TableKey(td.Schema, td.Table))
			b.WriteString(`<div class="part"><div class="path">`)
			switch td.Kind {
			case schemacompare.TableNew:
				b.WriteString(`+ NEW TABLE ` + name)
			case schemacompare.TableRemoved:
				b.WriteString(`- REMOVED ` + name)
			default:
				b.WriteString(name)
			}
			b.WriteString(`</div><pre>`)
			for _, cc := range td.Columns {
				cls, line := "ctx", ""
				switch cc.Kind {
				case schemacompare.ColAdded:
					cls, line = "add", fmt.Sprintf("+ %s: %s", cc.Name, cc.NewType)
				case schemacompare.ColRemoved:
					cls, line = "rem", fmt.Sprintf("- %s: %s", cc.Name, cc.OldType)
				case schemacompare.ColTypeChanged:
					cls, line = "ctx", fmt.Sprintf("~ %s: %s → %s", cc.Name, cc.OldType, cc.NewType)
				}
				fmt.Fprintf(&b, `<span class="ln %s">%s</span>`, cls, html.EscapeString(line))
			}
			b.WriteString(`</pre></div>`)
		}
		b.WriteString(`</details>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}
