package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
)

// DiffLine is one line of a unified diff. Op is ' ' (context), '-' (only in
// old), or '+' (only in new). OldNo/NewNo are the line's 1-based position in
// the old/new content; 0 means the line has no position on that side ('+'
// lines have no OldNo, '-' lines no NewNo, fold markers neither).
type DiffLine struct {
	Op    byte
	Text  string
	OldNo int
	NewNo int
}

// commonAffixLen counts the leading (prefix) and trailing (suffix) lines that a
// and b share. The two regions never overlap: suffix only counts within what the
// prefix leaves, so identical inputs report prefix=len, suffix=0.
func commonAffixLen(a, b []string) (prefix, suffix int) {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for prefix < limit && a[prefix] == b[prefix] {
		prefix++
	}
	for suffix < limit-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	return
}

// unifiedLineDiff computes a line-level diff of old→new. It first strips the
// common prefix/suffix (emitted verbatim as context) so the O(n×m) LCS only runs
// over the divergent core — a tiny edit in a huge file diffs in milliseconds
// instead of allocating a file²-sized table. At a divergence removed lines
// precede added lines.
func unifiedLineDiff(oldText, newText string) []DiffLine {
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")
	prefix, suffix := commonAffixLen(a, b)

	var out []DiffLine
	for i := 0; i < prefix; i++ {
		out = append(out, DiffLine{Op: ' ', Text: a[i], OldNo: i + 1, NewNo: i + 1})
	}
	out = append(out, lcsDiff(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix], prefix, prefix)...)
	for i := len(a) - suffix; i < len(a); i++ {
		newNo := len(b) - suffix + (i - (len(a) - suffix))
		out = append(out, DiffLine{Op: ' ', Text: a[i], OldNo: i + 1, NewNo: newNo + 1})
	}
	return out
}

// lcsDiff diffs two line slices via a longest-common-subsequence table, emitting
// context/removed/added lines in order. Callers pass the divergent core only, so
// the len(a)×len(b) table stays bounded. Cells are int32 (counts never exceed
// min(len(a),len(b)) ≪ 2³¹), halving the table's footprint vs int.
func lcsDiff(a, b []string, aOff, bOff int) []DiffLine {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, DiffLine{Op: ' ', Text: a[i], OldNo: aOff + i + 1, NewNo: bOff + j + 1})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			out = append(out, DiffLine{Op: '-', Text: a[i], OldNo: aOff + i + 1})
			i++
		} else {
			out = append(out, DiffLine{Op: '+', Text: b[j], NewNo: bOff + j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{Op: '-', Text: a[i], OldNo: aOff + i + 1})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{Op: '+', Text: b[j], NewNo: bOff + j + 1})
	}
	return out
}

// maxDiffCells caps the LCS table area (coreOld × coreNew), where the core is
// what's left after stripping the common prefix/suffix. This guards LCS COST
// only (rendered size is bounded separately by maxRenderedDiffLines), so we cap
// the table's AREA, not either dimension: scattered edits in a big notebook
// leave a large core that's still cheap (the 3400-line, ~2300-core real case is
// ~5M cells), and a lopsided diff (thousands deleted, few added) stays cheap
// too — only a near-total rewrite of a huge file, the genuinely expensive case,
// trips it. 40M int32 cells ≈ 160 MB, a sane ceiling for a one-off CLI render.
const maxDiffCells = 40_000_000

// contextLines is how many unchanged lines foldContext keeps on each side of a
// change; the rest of every long unchanged run collapses into one marker.
const contextLines = 3

// maxRenderedDiffLines bounds the folded diff that actually reaches the HTML
// report. The area cap bounds LCS cost but not output size: a lopsided diff
// (e.g. a wholesale-new part, 1×N core) sails under it, and folding only
// collapses unchanged lines, never '+'/'-' — so a minified multi-MB JSON
// pretty-printed to hundreds of thousands of lines would render one <span> per
// line and freeze the browser tab. 10k lines is comfortably browsable and far
// above any diff a human will actually read.
const maxRenderedDiffLines = 10_000

// cappedLineDiff is unifiedLineDiff guarded twice: an area cap on the divergent
// core (LCS cost — past it, a single summary line), and a rendered-lines cap on
// the folded output (browser cost — past it, the head plus a truncation marker).
func cappedLineDiff(oldText, newText string) []DiffLine {
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")
	prefix, suffix := commonAffixLen(a, b)
	coreOld := len(a) - prefix - suffix
	coreNew := len(b) - prefix - suffix
	if int64(coreOld)*int64(coreNew) > maxDiffCells {
		return []DiffLine{{Op: ' ', Text: fmt.Sprintf(
			"Change too large to diff inline — %d → %d divergent lines (out of %d → %d total).",
			coreOld, coreNew, len(a), len(b))}}
	}
	folded := foldContext(unifiedLineDiff(oldText, newText), contextLines)
	if len(folded) > maxRenderedDiffLines {
		omitted := len(folded) - maxRenderedDiffLines
		folded = append(folded[:maxRenderedDiffLines:maxRenderedDiffLines],
			DiffLine{Op: '@', Text: fmt.Sprintf("⋯ diff truncated — %d more lines omitted ⋯", omitted)})
	}
	return folded
}

// foldContext collapses long runs of unchanged lines into a single fold marker
// ({'@', "⋯ N unchanged lines ⋯"}), keeping ctx lines of context on each side of
// every change so the reader lands on the edit instead of scrolling thousands of
// identical lines. Input with no changes passes through unchanged.
func foldContext(lines []DiffLine, ctx int) []DiffLine {
	keep := make([]bool, len(lines))
	changed := false
	for i, l := range lines {
		if l.Op != '-' && l.Op != '+' {
			continue
		}
		changed = true
		keep[i] = true
		for d := 1; d <= ctx; d++ {
			if i-d >= 0 {
				keep[i-d] = true
			}
			if i+d < len(lines) {
				keep[i+d] = true
			}
		}
	}
	if !changed {
		return lines
	}

	var out []DiffLine
	for i := 0; i < len(lines); {
		if keep[i] {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && !keep[j] {
			j++
		}
		n := j - i
		noun := "lines"
		if n == 1 {
			noun = "line"
		}
		out = append(out, DiffLine{Op: '@', Text: fmt.Sprintf("⋯ %d unchanged %s ⋯", n, noun)})
		i = j
	}
	return out
}

// lineNoCell renders one line-number gutter cell; 0 (no position on that side)
// renders as an empty cell so the gutter stays aligned.
func lineNoCell(n int) string {
	if n == 0 {
		return `<span class="no"></span>`
	}
	return fmt.Sprintf(`<span class="no">%d</span>`, n)
}

// prettyForDiff pretty-prints content as 2-space-indented JSON when it parses
// as JSON, so minified/awkward Fabric .json parts diff readably and pure
// formatting differences collapse. Non-JSON content is returned unchanged.
// The bool reports whether pretty-printing was applied (i.e. the input was
// valid JSON) — callers use it instead of a separate isJSON check.
func prettyForDiff(content string) (string, bool) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(content), "", "  "); err != nil {
		return content, false
	}
	return buf.String(), true
}

// deployReportStyle is the verbatim <style> block from the mockup, with two
// extra card variants (.card.unchanged, .card.exists) for the preview view.
const deployReportStyle = `<style>
  :root{
    --green:#4ade80; --green-bright:#86efac; --green-deep:#22c55e;
    --changed:#fbbf24; --deleted:#f87171; --fail:#ef4444; --warn:#fbbf24; --exists:#2dd4bf; --unchanged:#8a978d;
    --text:#dce7df; --muted:#7e8d82; --accent:#86efac;
    --panel-line:rgba(120,200,150,.14);
    --addfg:#86efac; --delfg:#fca5a5;
  }
  *{box-sizing:border-box}
  body{
    font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
    color:var(--text);margin:0;padding:1.8rem 2rem 3rem;line-height:1.45;
    background:
      radial-gradient(1100px 480px at 12% -8%, rgba(34,197,94,.16), transparent 60%),
      radial-gradient(900px 520px at 100% 0%, rgba(45,212,191,.08), transparent 55%),
      linear-gradient(165deg,#0c1611 0%,#0a0f0c 55%,#080b09 100%);
    background-attachment:fixed;min-height:100vh;
  }
  code,pre,.mono{font-family:"SF Mono",Menlo,Consolas,monospace}

  /* ── hero header ── */
  .hero{margin-bottom:1.5rem}
  h1{font-size:1.45rem;margin:0;font-weight:700;letter-spacing:-.01em;
     background:linear-gradient(92deg,#86efac,#34d399 60%,#2dd4bf);
     -webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent}
  .sub{color:var(--muted);font-size:.85rem;margin-top:.35rem}
  .sub b{color:#bfe9cd;font-weight:600}
  .hero .when{color:var(--muted);font-size:.85rem;margin-top:.35rem;font-variant-numeric:tabular-nums}
  h2{font-size:.95rem;margin:1.9rem 0 .55rem;font-weight:600;letter-spacing:.01em;
     color:#bff0cf;display:flex;align-items:center;gap:.5rem}
  h2::before{content:"";width:.55rem;height:.55rem;border-radius:2px;
     background:linear-gradient(135deg,var(--green-bright),var(--green-deep));box-shadow:0 0 10px rgba(74,222,128,.6)}
  h2 .note{color:var(--muted);font-weight:400;font-size:.82rem}

  /* ── summary cards ── */
  .cards{display:flex;flex-wrap:wrap;gap:.85rem;margin:.5rem 0}
  .card{flex:1 1 120px;min-width:120px;position:relative;border-radius:14px;padding:.95rem 1.05rem;
        border:1px solid var(--panel-line);overflow:hidden;
        background:linear-gradient(150deg,rgba(255,255,255,.05),rgba(255,255,255,.012));
        box-shadow:0 6px 22px rgba(0,0,0,.35), inset 0 1px 0 rgba(255,255,255,.04)}
  .card::before{content:"";position:absolute;inset:0 auto 0 0;width:3px;border-radius:14px 0 0 14px}
  .card::after{content:"";position:absolute;top:-40%;right:-30%;width:140px;height:140px;border-radius:50%;
        opacity:.16;filter:blur(26px)}
  .card .n{font-size:1.9rem;font-weight:750;line-height:1;letter-spacing:-.02em}
  .card .l{font-size:.72rem;color:var(--muted);text-transform:uppercase;letter-spacing:.07em;margin-top:.45rem;font-weight:600}
  .card.new::before{background:linear-gradient(var(--green-bright),var(--green-deep))} .card.new::after{background:var(--green)} .card.new .n{color:var(--green-bright)}
  .card.changed::before{background:linear-gradient(#fde68a,#f59e0b)} .card.changed::after{background:var(--changed)} .card.changed .n{color:var(--changed)}
  .card.deleted::before{background:linear-gradient(#fda4a4,#dc2626)} .card.deleted::after{background:var(--deleted)} .card.deleted .n{color:var(--deleted)}
  .card.fail::before{background:linear-gradient(#fca5a5,#b91c1c)} .card.fail::after{background:var(--fail)} .card.fail .n{color:var(--fail)}
  .card.warn::before{background:linear-gradient(#fde68a,#d97706)} .card.warn::after{background:var(--warn)} .card.warn .n{color:var(--warn)}
  .card.unchanged::before{background:linear-gradient(#b0bab4,#6b7875)} .card.unchanged::after{background:var(--unchanged)} .card.unchanged .n{color:var(--unchanged)}
  .card.exists::before{background:linear-gradient(#5eead4,#0d9488)} .card.exists::after{background:var(--exists)} .card.exists .n{color:var(--exists)}

  /* ── results tables ── */
  .panel{border:1px solid var(--panel-line);border-radius:12px;overflow:hidden;
         background:linear-gradient(160deg,rgba(255,255,255,.028),rgba(255,255,255,.006))}
  table{border-collapse:collapse;width:100%;font-size:.88rem}
  td{padding:.42rem .9rem;border-bottom:1px solid rgba(255,255,255,.05);vertical-align:top}
  tr:last-child td{border-bottom:none}
  td.mark{width:1.6rem;text-align:center;font-weight:700}
  td.name{font-family:"SF Mono",Menlo,monospace}
  .type{color:var(--muted);font-size:.78rem}
  .ok{color:var(--green-bright)} .upd{color:var(--changed)} .del{color:var(--deleted)}
  .efail{color:var(--fail)} .ewarn{color:var(--warn)}
  .detail{color:var(--muted);font-size:.82rem}
  .detail.efail{color:var(--fail)} .detail.ewarn{color:var(--warn)}

  /* ── diff items ── */
  .item{border:1px solid var(--panel-line);border-radius:12px;margin:.6rem 0;overflow:hidden;position:relative;
        background:linear-gradient(160deg,rgba(255,255,255,.03),rgba(255,255,255,.007));
        box-shadow:0 4px 16px rgba(0,0,0,.28)}
  .item::before{content:"";position:absolute;top:0;bottom:0;left:0;width:3px}
  .item.changed::before{background:linear-gradient(#fde68a,#f59e0b)}
  .item.new::before{background:linear-gradient(var(--green-bright),var(--green-deep))}
  .item summary{cursor:pointer;padding:.6rem .95rem;font-weight:600;list-style:none;display:flex;align-items:center;gap:.6rem}
  .item summary::-webkit-details-marker{display:none}
  .dot{width:.55rem;height:.55rem;border-radius:50%;flex:0 0 auto}
  .dot.changed{background:var(--changed);box-shadow:0 0 8px rgba(251,191,36,.55)}
  .dot.new{background:var(--green);box-shadow:0 0 8px rgba(74,222,128,.55)}
  .item summary .t{color:var(--muted);font-size:.78rem;font-weight:500}
  .item summary .chev{margin-left:auto;color:var(--muted);font-size:.78rem}
  .part{border-top:1px solid var(--panel-line)}
  .part .path{color:#d8b3ea;padding:.4rem .95rem;font-size:.8rem;display:flex;align-items:center;gap:.55rem;
              background:linear-gradient(90deg,rgba(255,255,255,.03),transparent)}
  .badge{font-size:.64rem;background:linear-gradient(135deg,rgba(45,212,191,.25),rgba(34,197,94,.18));
         color:#9ff0d6;border:1px solid rgba(45,212,191,.25);border-radius:5px;padding:.08rem .45rem;
         text-transform:uppercase;letter-spacing:.04em}
  .badge.cap{background:linear-gradient(135deg,rgba(251,191,36,.22),rgba(217,119,6,.15));color:#fcd66b;border-color:rgba(251,191,36,.3)}
  .difftools{display:flex;gap:.45rem;margin-left:auto}
  .btn{font:inherit;font-size:.74rem;color:#bff0cf;background:linear-gradient(150deg,rgba(74,222,128,.14),rgba(74,222,128,.04));
       border:1px solid rgba(74,222,128,.25);border-radius:7px;padding:.25rem .6rem;cursor:pointer;transition:.15s}
  .btn:hover{background:linear-gradient(150deg,rgba(74,222,128,.24),rgba(74,222,128,.08))}
  .h2row{display:flex;align-items:center;gap:.5rem;margin:1.9rem 0 .55rem}
  .h2row h2{margin:0}
  pre{margin:0;padding:.5rem 0;overflow-x:auto;font-size:.82rem;line-height:1.45}
  pre .ln{display:block;padding:0 .95rem;white-space:pre}
  pre .no{display:inline-block;width:3.2em;text-align:right;padding-right:.7em;
          color:#55635a;font-size:.9em;user-select:none}
  pre .ctx{color:#8fa096}
  pre .add{color:var(--addfg);background:linear-gradient(90deg,rgba(34,197,94,.16),rgba(34,197,94,.04))}
  pre .rem{color:var(--delfg);background:linear-gradient(90deg,rgba(239,68,68,.15),rgba(239,68,68,.03))}
  pre .fold{color:#6b7a70;font-style:italic;background:rgba(255,255,255,.022);border-top:1px solid rgba(255,255,255,.04);border-bottom:1px solid rgba(255,255,255,.04)}
  .empty{color:var(--muted);padding:1rem .95rem}
  .foot{color:#5d6b61;font-size:.74rem;margin-top:2.4rem;border-top:1px solid var(--panel-line);padding-top:.8rem}
  .foot code{color:#9ff0d6}
  .wsgroup{font-size:.78rem;font-weight:600;color:var(--muted);letter-spacing:.04em;
            margin:.9rem 0 .3rem;padding:.18rem .5rem;
            border-left:2px solid var(--green-deep);
            background:linear-gradient(90deg,rgba(34,197,94,.07),transparent)}
</style>`

// renderDeployReport builds a self-contained HTML deploy report. When results
// is non-nil it leads with a per-item outcome section (✓ deployed / ⚠ warning /
// ✗ error); it always follows with every Changed item's per-part content diff
// (old=deployed, new=local). When postRuns is non-empty, a trailing
// "Post-deploy runs" section reports each notebook run (✓ completed / ✗ failed
// / ⊘ skipped). All content is HTML-escaped. With results==nil it is the
// compare-only viewer the browser preview shows.
func renderDeployReport(groups []deployGroup, results []deploy.Result, postRuns []postDeployOutcome, ts time.Time) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>futils deploy report</title>`)
	b.WriteString(deployReportStyle)
	b.WriteString(`</head><body>`)

	// Hero header with the deploy timestamp.
	b.WriteString(`<div class="hero"><h1>futils deploy report</h1>`)
	b.WriteString(`<div class="when">` + ts.Format("2006-01-02 15:04") + `</div></div>`)

	// Summary cards.
	b.WriteString(renderSummaryCards(groups, results))

	if results != nil {
		var deployed, deleted []deploy.Result
		for _, r := range results {
			if r.Action == deploy.ActionDelete {
				deleted = append(deleted, r)
			} else {
				deployed = append(deployed, r)
			}
		}
		renderRows := func(heading string, rs []deploy.Result) {
			if len(rs) == 0 {
				return
			}
			b.WriteString(`<h2>` + heading + `</h2><div class="panel"><table>`)
			for _, r := range rs {
				markCls, mark, detailCls, detail := "ok", "✓", "detail", r.Action.String()
				switch {
				case r.Err != nil:
					markCls, mark, detailCls, detail = "efail", "✗", "detail efail", r.Err.Error()
				case r.Warning != "":
					markCls, mark, detailCls, detail = "ewarn", "⚠", "detail ewarn", r.Warning
				case r.Action == deploy.ActionUpdate:
					markCls = "upd"
				case r.Action == deploy.ActionDelete:
					markCls = "del"
				}
				b.WriteString(`<tr><td class="mark ` + markCls + `">` + mark + `</td>`)
				b.WriteString(`<td class="name">` + html.EscapeString(r.Name) + ` <span class="type">` + html.EscapeString(r.Type) + `</span></td>`)
				b.WriteString(`<td class="` + detailCls + `">` + html.EscapeString(detail) + `</td></tr>`)
			}
			b.WriteString(`</table></div>`)
		}
		renderRows("Deployed items", deployed)
		renderRows("Deleted items", deleted)
	}

	// Post-deploy runs section — same panel/table look as the results sections.
	if len(postRuns) > 0 {
		b.WriteString(`<h2>Post-deploy runs</h2><div class="panel"><table>`)
		for _, o := range postRuns {
			markCls, mark, detailCls, detail := "ok", "✓", "detail", "Completed in "+o.Duration.String()
			switch {
			case o.Status == postDeployStatusSkipped:
				markCls, mark, detailCls, detail = "del", "⊘", "detail", "skipped — earlier run failed"
			case o.Err != nil:
				markCls, mark, detailCls, detail = "efail", "✗", "detail efail", o.Err.Error()
			}
			b.WriteString(`<tr><td class="mark ` + markCls + `">` + mark + `</td>`)
			b.WriteString(`<td class="name">` + html.EscapeString(o.Run.Name) + ` <span class="type">` + html.EscapeString(o.Run.WorkspaceName) + `</span></td>`)
			b.WriteString(`<td class="` + detailCls + `">` + html.EscapeString(detail) + `</td></tr>`)
		}
		b.WriteString(`</table></div>`)
	}

	// Build the deployed-items gate for fix [6]: when results != nil, only render
	// diffs for items that were actually deployed (i.e. not deleted).
	// Key is type+"\x00"+name — mirrors internal/deploy's item-identity convention,
	// since Fabric allows duplicate display names across different item types.
	var deployedSet map[string]bool
	if results != nil {
		deployedSet = make(map[string]bool, len(results))
		for _, r := range results {
			if r.Action != deploy.ActionDelete {
				deployedSet[r.Type+"\x00"+r.Name] = true
			}
		}
	}

	// itemRenderable reports whether a diff item passes the deployed-set gate.
	// When deployedSet is nil (preview), all items render.
	itemRenderable := func(it ItemDiff) bool {
		return deployedSet == nil || deployedSet[it.Type+"\x00"+it.Name]
	}

	// Count how many diffs will actually render (respecting the gate), and how
	// many groups contribute at least one rendered diff (for per-workspace headings).
	changed := 0
	groupsWithDiffs := 0
	for _, g := range groups {
		groupCount := 0
		for _, it := range g.Diffs {
			if itemRenderable(it) {
				changed++
				groupCount++
			}
		}
		if groupCount > 0 {
			groupsWithDiffs++
		}
	}

	// Content diffs heading with inline expand/collapse controls.
	// mockup uses inline onclick, not a <script> tag.
	b.WriteString(`<div class="h2row">`)
	b.WriteString(fmt.Sprintf(`<h2>Content diffs <span class="note">— deployed → local · %d changed item(s)</span></h2>`, changed))
	b.WriteString(`<div class="difftools">`)
	b.WriteString(`<button class="btn" onclick="document.querySelectorAll('.item').forEach(d=&gt;d.open=true)">Expand all</button>`)
	b.WriteString(`<button class="btn" onclick="document.querySelectorAll('.item').forEach(d=&gt;d.open=false)">Collapse all</button>`)
	b.WriteString(`</div></div>`)

	for _, g := range groups {
		if len(g.Diffs) == 0 {
			continue
		}
		// Check whether this group contributes any rendered items before emitting
		// the per-workspace heading (avoid orphan headings with no diffs below them).
		hasRenderable := false
		for _, it := range g.Diffs {
			if itemRenderable(it) {
				hasRenderable = true
				break
			}
		}
		if !hasRenderable {
			continue
		}
		if groupsWithDiffs > 1 {
			b.WriteString(`<div class="wsgroup">` + html.EscapeString(g.Target.DisplayName) + `</div>`)
		}
		for _, it := range g.Diffs {
			// Fix [6]: skip items not in the deployed set when results are present.
			if !itemRenderable(it) {
				continue
			}
			b.WriteString(`<details class="item changed">`)
			b.WriteString(`<summary><span class="dot changed"></span>`)
			b.WriteString(html.EscapeString(it.Name))
			b.WriteString(` <span class="t">` + html.EscapeString(it.Type) + `</span>`)
			b.WriteString(`<span class="chev">▾</span></summary>`)
			for _, p := range it.Parts {
				oldPretty, oldIsJSON := prettyForDiff(p.Old)
				newPretty, newIsJSON := prettyForDiff(p.New)
				badge := ""
				if oldIsJSON || newIsJSON {
					badge = ` <span class="badge">json · prettified</span>`
				}
				b.WriteString(`<div class="part"><div class="path">` + html.EscapeString(p.Path) + badge + `</div><pre>`)
				for _, ln := range cappedLineDiff(oldPretty, newPretty) {
					cls, prefix := "ctx", " "
					switch ln.Op {
					case '-':
						cls, prefix = "rem", "-"
					case '+':
						cls, prefix = "add", "+"
					case '@':
						cls, prefix = "fold", " "
					}
					b.WriteString(`<span class="ln ` + cls + `">` + lineNoCell(ln.OldNo) + lineNoCell(ln.NewNo) + prefix + " " + html.EscapeString(ln.Text) + "</span>")
				}
				b.WriteString(`</pre></div>`)
			}
			b.WriteString(`</details>`)
		}
	}
	if changed == 0 {
		b.WriteString(`<div class="empty">No changed items to diff.</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

// renderDeployDiffHTML is the compare-only view (no deploy results) used by the
// in-browser preview.
func renderDeployDiffHTML(groups []deployGroup) string {
	return renderDeployReport(groups, nil, nil, time.Now())
}

// renderSummaryCards builds the colored summary-card row. With results it shows
// the deploy OUTCOME (Created/Updated/Deleted/Failed/Warnings); without (the
// compare preview) it shows the CLASSIFICATION (New/Changed/Orphan/Unchanged/Exists).
func renderSummaryCards(groups []deployGroup, results []deploy.Result) string {
	type card struct {
		n          int
		label, cls string
	}
	var cards []card
	if results != nil {
		var created, updated, deleted, failed, warned int
		for _, r := range results {
			switch {
			case r.Err != nil:
				failed++
			case r.Action == deploy.ActionDelete:
				deleted++
			case r.Action == deploy.ActionUpdate:
				updated++
				if r.Warning != "" {
					warned++
				}
			default:
				created++
				if r.Warning != "" {
					warned++
				}
			}
		}
		cards = []card{{created, "Created", "new"}, {updated, "Updated", "changed"}, {deleted, "Deleted", "deleted"}}
		if failed > 0 {
			cards = append(cards, card{failed, "Failed", "fail"})
		}
		if warned > 0 {
			cards = append(cards, card{warned, "Warnings", "warn"})
		}
	} else {
		c := countByClass(groups)
		cards = []card{
			{c[deploy.ClassNew], "New", "new"},
			{c[deploy.ClassChanged], "Changed", "changed"},
			{c[deploy.ClassOrphan], "Orphan", "deleted"},
			{c[deploy.ClassUnchanged], "Unchanged", "unchanged"},
		}
		if c[deploy.ClassExists] > 0 {
			cards = append(cards, card{c[deploy.ClassExists], "Exists", "exists"})
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="cards">`)
	for _, cd := range cards {
		b.WriteString(fmt.Sprintf(`<div class="card %s"><div class="n">%d</div><div class="l">%s</div></div>`,
			cd.cls, cd.n, cd.label))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// openInBrowser opens a local file in the OS default browser.
func openInBrowser(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// showDiffsInBrowser renders the diff HTML to a temp file and opens it in the
// browser. The file is deliberately NOT deleted afterwards: any timer races the
// browser's load (a cold start can take >5s and would land on file-not-found,
// with no way back to the report). It lives in the OS temp dir, which the OS
// cleans on its own — it is an ephemeral viewer, not a saved artifact.
func showDiffsInBrowser(groups []deployGroup) error {
	htmlDoc := renderDeployDiffHTML(groups)
	f, err := os.CreateTemp("", "futils-deploy-diff-*.html")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(htmlDoc); err != nil {
		f.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	f.Close()
	if err := openInBrowser(path); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
