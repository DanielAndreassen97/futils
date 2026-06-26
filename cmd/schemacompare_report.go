package cmd

import (
	"fmt"
	"html"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// schemaCompareStyle adds the structured-grid classes the schema-compare report
// needs on top of deployReportStyle (which supplies the palette vars, body
// background, hero, and the collapsible .item card). Kept separate so the
// deploy report's style isn't polluted with schema-compare-only rules.
const schemaCompareStyle = `<style>
  .sc-trow{display:flex;align-items:center;gap:.5rem;font-family:"SF Mono",Menlo,monospace;font-size:.82rem;padding:.4rem .95rem;color:#e7f3ea}
  .sc-trow .sc-mk{font-weight:700}
  .sc-trow.new .sc-mk{color:var(--addfg)} .sc-trow.del .sc-mk{color:var(--delfg)}
  .sc-chip{font-family:"SF Mono",Menlo,monospace;font-size:.62rem;font-weight:700;text-transform:uppercase;letter-spacing:.05em;border-radius:5px;padding:.08rem .42rem}
  .sc-chip.new{color:#0a0f0c;background:var(--green-bright)}
  .sc-chip.del{color:#1a0c0c;background:var(--deleted)}
  .sc-tname{font-family:"SF Mono",Menlo,monospace;font-size:.82rem;color:#e7f3ea;font-weight:600;padding:.6rem .95rem .3rem}
  .sc-scroll{overflow-x:auto;border:1px solid var(--panel-line);border-radius:9px;margin:0 .95rem .65rem}
  table.sc-cols{border-collapse:collapse;width:100%;font-family:"SF Mono",Menlo,monospace;font-size:.8rem}
  .sc-cols td{padding:.32rem .7rem;border-bottom:1px solid rgba(255,255,255,.05);vertical-align:top;white-space:nowrap}
  .sc-cols tr:last-child td{border-bottom:none}
  .sc-cols .mk{width:1.4rem;text-align:center;font-weight:700}
  .sc-cols .ty{color:var(--muted);font-variant-numeric:tabular-nums}
  .sc-cols tr.add .mk,.sc-cols tr.add .col{color:var(--addfg)}
  .sc-cols tr.rem .mk,.sc-cols tr.rem .col{color:var(--delfg)}
  .sc-cols tr.chg .mk,.sc-cols tr.chg .col{color:var(--changed)}
  .sc-cols .arrow{color:#5d6b61;padding:0 .35rem}
  .sc-cols .from{color:var(--delfg)} .sc-cols .to{color:var(--addfg)}
  .sc-pill{font-family:"SF Mono",Menlo,monospace;font-size:.7rem;border-radius:20px;padding:.12rem .6rem;font-weight:600;color:var(--addfg);border:1px solid rgba(134,239,172,.3);background:rgba(34,197,94,.1)}
</style>`

// renderSchemaCompareReport builds a self-contained HTML report. It reuses the
// deploy report's <style> (palette, body, hero, collapsible .item card) plus
// schemaCompareStyle, and renders each lakehouse as a collapsible card: added /
// removed tables as chipped rows, and each changed table as a column grid with
// an explicit from → to for type changes. All content is HTML-escaped.
func renderSchemaCompareReport(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>futils schema compare</title>`)
	b.WriteString(deployReportStyle)
	b.WriteString(schemaCompareStyle)
	b.WriteString(`</head><body>`)
	b.WriteString(`<div class="hero"><h1>futils schema compare</h1>`)
	es, et := html.EscapeString(srcLabel), html.EscapeString(tgtLabel)
	fmt.Fprintf(&b, `<div class="sub"><b>%s</b> → <b>%s</b> · `+
		`<span style="color:var(--addfg)">+ only in %s</span> · `+
		`<span style="color:var(--delfg)">− only in %s</span> · `+
		`<span style="color:var(--changed)">~ type changed</span></div></div>`,
		es, et, es, et)

	for _, lh := range diffs {
		b.WriteString(`<details class="item changed" open><summary><span class="dot changed"></span>`)
		b.WriteString(html.EscapeString(lh.Lakehouse))
		scope := html.EscapeString(strings.Join(lh.Schemas, ", "))

		if len(lh.Tables) == 0 {
			fmt.Fprintf(&b, ` <span class="t">%s</span>`+
				`<span class="sc-pill" style="margin-left:auto">✓ identical · %d tables</span>`+
				`<span class="chev">▾</span></summary></details>`, scope, lh.Matching)
			continue
		}

		fmt.Fprintf(&b, ` <span class="t">%s · %d unchanged</span><span class="chev">▾</span></summary>`, scope, lh.Matching)
		for _, td := range lh.Tables {
			name := html.EscapeString(schemacompare.TableKey(td.Schema, td.Table))
			switch td.Kind {
			case schemacompare.TableNew:
				fmt.Fprintf(&b, `<div class="sc-trow new"><span class="sc-mk">+</span>%s<span class="sc-chip new">new table</span></div>`, name)
			case schemacompare.TableRemoved:
				fmt.Fprintf(&b, `<div class="sc-trow del"><span class="sc-mk">−</span>%s<span class="sc-chip del">removed</span></div>`, name)
			case schemacompare.TableChanged:
				fmt.Fprintf(&b, `<div class="sc-tname">%s</div><div class="sc-scroll"><table class="sc-cols">`, name)
				for _, cc := range td.Columns {
					col := html.EscapeString(cc.Name)
					switch cc.Kind {
					case schemacompare.ColAdded:
						fmt.Fprintf(&b, `<tr class="add"><td class="mk">+</td><td class="col">%s</td><td class="ty">%s</td></tr>`,
							col, html.EscapeString(cc.NewType))
					case schemacompare.ColRemoved:
						fmt.Fprintf(&b, `<tr class="rem"><td class="mk">−</td><td class="col">%s</td><td class="ty">%s</td></tr>`,
							col, html.EscapeString(cc.OldType))
					case schemacompare.ColTypeChanged:
						fmt.Fprintf(&b, `<tr class="chg"><td class="mk">~</td><td class="col">%s</td>`+
							`<td class="ty"><span class="from">%s</span><span class="arrow">→</span><span class="to">%s</span></td></tr>`,
							col, html.EscapeString(cc.OldType), html.EscapeString(cc.NewType))
					}
				}
				b.WriteString(`</table></div>`)
			}
		}
		b.WriteString(`</details>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}
