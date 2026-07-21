package cmd

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// schemaCompareStyle adds the schema-compare-only rules on top of
// deployReportStyle (palette vars, body background, cards, the collapsible
// .item card). The report's own hero (kicker + env route) replaces the deploy
// hero, so h1 is overridden from the gradient headline to a small kicker.
// Kept separate so the deploy report's style isn't polluted with
// schema-compare-only rules.
const schemaCompareStyle = `<style>
  h1{font-size:1.02rem;margin:0;font-weight:600;letter-spacing:.14em;text-transform:uppercase;
     background:none;-webkit-text-fill-color:currentColor;color:#5f7266;
     display:flex;align-items:center;gap:.6rem}
  h1::before{content:"";width:.6rem;height:.6rem;border-radius:2px;
     background:linear-gradient(135deg,var(--green-bright),var(--green-deep));box-shadow:0 0 12px rgba(74,222,128,.7)}
  h1 .when{margin-left:auto;font-size:.78rem;letter-spacing:0;text-transform:none;color:var(--muted);font-variant-numeric:tabular-nums;font-weight:400}
  .sc-route{display:flex;align-items:center;gap:1.1rem;margin:1rem 0 1.6rem;flex-wrap:wrap}
  .sc-env{font-family:"SF Mono",Menlo,monospace;font-size:1.28rem;font-weight:700;letter-spacing:-.01em;
       padding:.5rem .95rem;border-radius:12px;border:1px solid var(--panel-line);
       background:linear-gradient(160deg,rgba(255,255,255,.05),rgba(255,255,255,.01));
       box-shadow:0 6px 22px rgba(0,0,0,.35)}
  .sc-env.src{color:var(--green-bright)}
  .sc-env.tgt{color:#9ff0d6}
  .sc-flow{flex:0 0 auto;display:flex;align-items:center;color:var(--green-deep)}
  .sc-flow svg{display:block}
  .sc-flow .dash{stroke-dasharray:6 7;animation:sc-flow 1.4s linear infinite}
  @keyframes sc-flow{to{stroke-dashoffset:-13}}
  @media (prefers-reduced-motion: reduce){.sc-flow .dash{animation:none}}
  .card .n .dim{color:var(--muted);font-size:1rem;font-weight:500}

  .item summary .chev{transition:transform .15s}
  .item[open] summary .chev{transform:rotate(180deg)}
  .sc-chips{margin-left:auto;display:flex;gap:.4rem;font-family:"SF Mono",Menlo,monospace;font-size:.72rem;font-weight:700}
  .sc-chips span{border-radius:6px;padding:.14rem .5rem;border:1px solid transparent}
  .sc-chips .add{color:var(--addfg);background:rgba(34,197,94,.12);border-color:rgba(134,239,172,.25)}
  .sc-chips .chg{color:var(--changed);background:rgba(251,191,36,.1);border-color:rgba(251,191,36,.28)}
  .sc-chips .del{color:var(--delfg);background:rgba(239,68,68,.1);border-color:rgba(252,165,165,.25)}
  .sc-chips .okc{color:var(--exists);background:rgba(45,212,191,.08);border-color:rgba(45,212,191,.25);font-weight:600}

  .sc-schema{font-family:"SF Mono",Menlo,monospace;font-size:.72rem;font-weight:700;letter-spacing:.09em;text-transform:uppercase;
          color:#5f7266;padding:.55rem 1rem .25rem;border-top:1px solid var(--panel-line)}
  .sc-trow{display:flex;align-items:center;gap:.55rem;font-family:"SF Mono",Menlo,monospace;font-size:.82rem;padding:.34rem 1rem;color:#e7f3ea}
  .sc-trow .sc-mk{font-weight:700;width:1ch;text-align:center}
  .sc-trow.new .sc-mk,.sc-trow.new .nm{color:var(--addfg)}
  .sc-trow.del .sc-mk,.sc-trow.del .nm{color:var(--delfg)}
  .sc-chip{font-size:.62rem;font-weight:700;text-transform:uppercase;letter-spacing:.05em;border-radius:5px;padding:.08rem .42rem}
  .sc-trow.new .sc-chip{color:#0a0f0c;background:var(--green-bright)}
  .sc-trow.del .sc-chip{color:#1a0c0c;background:var(--deleted)}
  .sc-tchg{padding:.45rem 1rem .1rem;font-family:"SF Mono",Menlo,monospace;font-size:.82rem;font-weight:600;color:#e7f3ea;display:flex;gap:.55rem;align-items:baseline}
  .sc-tchg .sc-mk{color:var(--changed);font-weight:700}
  .sc-tchg .cc{color:var(--muted);font-weight:400;font-size:.78em}
  .sc-colgrid{margin:.25rem 1rem .7rem 2.1rem;border-left:2px solid rgba(251,191,36,.25);padding-left:.9rem;
           font-family:"SF Mono",Menlo,monospace;font-size:.8rem;overflow-x:auto}
  .sc-cg{display:grid;grid-template-columns:1.2rem minmax(15ch,20ch) 1fr;padding:.18rem 0;align-items:baseline;white-space:nowrap}
  .sc-cg .sc-mk{font-weight:700}
  .sc-cg.add .sc-mk,.sc-cg.add .col{color:var(--addfg)}
  .sc-cg.rem .sc-mk,.sc-cg.rem .col{color:var(--delfg)}
  .sc-cg.chg .sc-mk,.sc-cg.chg .col{color:var(--changed)}
  .sc-cg .ty{color:var(--muted)}
  .sc-cg .from{color:var(--delfg)} .sc-cg .to{color:var(--addfg)} .sc-cg .arrow{color:#5d6b61;padding:0 .4rem}

  .item.ok::before{background:linear-gradient(#5eead4,#0d9488)}
  .dot.ok{background:var(--exists);box-shadow:0 0 8px rgba(45,212,191,.55)}
</style>`

// scFlowArrow is the animated DEV→TEST stream in the hero: a dashed line
// crawling toward a solid arrowhead (paused under prefers-reduced-motion).
const scFlowArrow = `<span class="sc-flow"><svg width="72" height="14" viewBox="0 0 72 14" fill="none">` +
	`<line class="dash" x1="0" y1="7" x2="58" y2="7" stroke="currentColor" stroke-width="2"/>` +
	`<path d="M58 1 L70 7 L58 13 Z" fill="currentColor"/></svg></span>`

// renderSchemaCompareReport builds a self-contained HTML report. It reuses the
// deploy report's <style> (palette, cards, collapsible .item card) plus
// schemaCompareStyle, and renders: a kicker + env-route hero, table-level
// summary cards, then one collapsible card per lakehouse whose COLLAPSED row
// already answers "how much drift" via +/~/− chips. Tables group under their
// schema; each changed table is a column grid with an explicit from → to for
// type changes. The +/−/~ legend lives in the footer. All content is
// HTML-escaped.
func renderSchemaCompareReport(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) string {
	var b strings.Builder
	b.WriteString(reportHead("futils schema compare"))
	b.WriteString(deployReportStyle)
	b.WriteString(schemaCompareStyle)
	b.WriteString(`</head><body>`)

	es, et := html.EscapeString(srcLabel), html.EscapeString(tgtLabel)
	fmt.Fprintf(&b, `<h1>futils schema compare <span class="when">%s</span></h1>`, time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, `<div class="sc-route"><span class="sc-env src">%s</span>%s<span class="sc-env tgt">%s</span></div>`,
		es, scFlowArrow, et)

	// Summary cards: table-level counts across every compared lakehouse, so the
	// verdict is readable before scrolling any card.
	var newT, remT, chgT, identical int
	for _, lh := range diffs {
		if len(lh.Tables) == 0 {
			identical++
		}
		for _, td := range lh.Tables {
			switch td.Kind {
			case schemacompare.TableNew:
				newT++
			case schemacompare.TableRemoved:
				remT++
			case schemacompare.TableChanged:
				chgT++
			}
		}
	}
	b.WriteString(renderCardRow([]summaryCard{
		{n: newT, label: "New tables", cls: "new"},
		{n: chgT, label: "Changed tables", cls: "changed"},
		{n: remT, label: "Removed tables", cls: "deleted"},
		{n: identical, label: "Identical lakehouses", cls: "exists", dim: fmt.Sprintf(" / %d", len(diffs))},
	}))

	for _, lh := range diffs {
		scope := html.EscapeString(strings.Join(lh.Schemas, ", "))

		if len(lh.Tables) == 0 {
			// Identical lakehouses get the calm teal treatment — the amber
			// "changed" dot on a ✓-card read as a false alarm.
			b.WriteString(`<details class="item ok"><summary><span class="dot ok"></span>`)
			b.WriteString(html.EscapeString(lh.Lakehouse))
			fmt.Fprintf(&b, ` <span class="t">%s</span>`+
				`<span class="sc-chips"><span class="okc">✓ identical · %d tables</span></span>`+
				`<span class="chev">▾</span></summary></details>`, scope, lh.Matching)
			continue
		}

		var addN, delN, chgN int
		for _, td := range lh.Tables {
			switch td.Kind {
			case schemacompare.TableNew:
				addN++
			case schemacompare.TableRemoved:
				delN++
			case schemacompare.TableChanged:
				chgN++
			}
		}
		b.WriteString(`<details class="item changed" open><summary><span class="dot changed"></span>`)
		b.WriteString(html.EscapeString(lh.Lakehouse))
		fmt.Fprintf(&b, ` <span class="t">%s</span><span class="sc-chips">`, scope)
		if addN > 0 {
			fmt.Fprintf(&b, `<span class="add">+%d</span>`, addN)
		}
		if chgN > 0 {
			fmt.Fprintf(&b, `<span class="chg">~%d</span>`, chgN)
		}
		if delN > 0 {
			fmt.Fprintf(&b, `<span class="del">−%d</span>`, delN)
		}
		fmt.Fprintf(&b, `<span class="okc">%d ✓</span></span><span class="chev">▾</span></summary>`, lh.Matching)

		// Group tables under one header per schema. Sort locally rather than
		// trusting the input order — Compare interleaves kinds, and a group
		// header per consecutive run would repeat schemas otherwise.
		tables := append([]schemacompare.TableDiff(nil), lh.Tables...)
		sort.Slice(tables, func(i, j int) bool {
			if tables[i].Schema != tables[j].Schema {
				return tables[i].Schema < tables[j].Schema
			}
			return tables[i].Table < tables[j].Table
		})
		prevSchema := ""
		for _, td := range tables {
			if td.Schema != prevSchema {
				fmt.Fprintf(&b, `<div class="sc-schema">%s</div>`, html.EscapeString(td.Schema))
				prevSchema = td.Schema
			}
			name := html.EscapeString(td.Table)
			switch td.Kind {
			case schemacompare.TableNew:
				fmt.Fprintf(&b, `<div class="sc-trow new"><span class="sc-mk">+</span><span class="nm">%s</span><span class="sc-chip">new table</span></div>`, name)
			case schemacompare.TableRemoved:
				fmt.Fprintf(&b, `<div class="sc-trow del"><span class="sc-mk">−</span><span class="nm">%s</span><span class="sc-chip">removed</span></div>`, name)
			case schemacompare.TableChanged:
				fmt.Fprintf(&b, `<div class="sc-tchg"><span class="sc-mk">~</span>%s <span class="cc">%d column change(s)</span></div><div class="sc-colgrid">`,
					name, len(td.Columns))
				for _, cc := range td.Columns {
					col := html.EscapeString(cc.Name)
					switch cc.Kind {
					case schemacompare.ColAdded:
						fmt.Fprintf(&b, `<div class="sc-cg add"><span class="sc-mk">+</span><span class="col">%s</span><span class="ty">%s</span></div>`,
							col, html.EscapeString(cc.NewType))
					case schemacompare.ColRemoved:
						fmt.Fprintf(&b, `<div class="sc-cg rem"><span class="sc-mk">−</span><span class="col">%s</span><span class="ty">%s</span></div>`,
							col, html.EscapeString(cc.OldType))
					case schemacompare.ColTypeChanged:
						fmt.Fprintf(&b, `<div class="sc-cg chg"><span class="sc-mk">~</span><span class="col">%s</span>`+
							`<span class="ty"><span class="from">%s</span><span class="arrow">→</span><span class="to">%s</span></span></div>`,
							col, html.EscapeString(cc.OldType), html.EscapeString(cc.NewType))
					}
				}
				b.WriteString(`</div>`)
			}
		}
		b.WriteString(`</details>`)
	}

	fmt.Fprintf(&b, `<div class="foot">+ only in %s · − only in %s · ~ type changed · generated by futils</div>`, es, et)
	b.WriteString(`</body></html>`)
	return b.String()
}
