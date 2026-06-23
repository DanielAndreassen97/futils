package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/internal/deploy"
)

// DiffLine is one line of a unified diff. Op is ' ' (context), '-' (only in
// old), or '+' (only in new).
type DiffLine struct {
	Op   byte
	Text string
}

// unifiedLineDiff computes a line-level diff of old→new using a longest-common-
// subsequence over lines, emitting context/removed/added lines in order. At a
// divergence, removed lines precede added lines.
func unifiedLineDiff(oldText, newText string) []DiffLine {
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")

	// LCS length table.
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
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
			out = append(out, DiffLine{' ', a[i]})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			out = append(out, DiffLine{'-', a[i]})
			i++
		} else {
			out = append(out, DiffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{'+', b[j]})
	}
	return out
}

// maxDiffLines caps how many lines either side of a part may have before the
// (O(n×m)) line diff is skipped. A pathologically large part — e.g. a generated
// model.bim, or a minified JSON pretty-printed to millions of lines — would
// otherwise blow up both memory (the LCS table is len(a)×len(b) ints) and the
// HTML (one <span> per line, which chokes the browser). Past the cap we emit a
// single summary line instead. ~2000 keeps every realistic Fabric definition
// while skipping only the runaway cases.
const maxDiffLines = 2000

// cappedLineDiff is unifiedLineDiff guarded by a size cap: within the cap it
// returns the real line diff; past it, a single summary line so one huge change
// can't blow up the report.
func cappedLineDiff(oldText, newText string) []DiffLine {
	oldN := strings.Count(oldText, "\n") + 1
	newN := strings.Count(newText, "\n") + 1
	if oldN > maxDiffLines || newN > maxDiffLines {
		return []DiffLine{{' ', fmt.Sprintf(
			"Content differs: %d → %d lines — too large to diff inline (open the file locally).", oldN, newN)}}
	}
	return unifiedLineDiff(oldText, newText)
}

// prettyForDiff pretty-prints content as 2-space-indented JSON when it parses
// as JSON, so minified/awkward Fabric .json parts diff readably and pure
// formatting differences collapse. Non-JSON content is returned unchanged.
func prettyForDiff(content string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(content), "", "  "); err != nil {
		return content
	}
	return buf.String()
}

// isJSON reports whether content is valid JSON (drives the "prettified" badge).
func isJSON(content string) bool {
	return json.Valid([]byte(content))
}

// renderDeployReport builds a self-contained HTML deploy report. When results
// is non-nil it leads with a per-item outcome section (✓ deployed / ⚠ warning /
// ✗ error); it always follows with every Changed item's per-part content diff
// (old=deployed, new=local). All content is HTML-escaped. With results==nil it
// is the compare-only viewer the browser preview shows.
func renderDeployReport(groups []deployGroup, results []deploy.Result) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>futils deploy report</title><style>
body{font-family:-apple-system,Menlo,monospace;background:#1e1e1e;color:#ddd;margin:0;padding:1.5rem}
h1{font-size:1.1rem;color:#9cdcfe}h2{font-size:1rem;color:#4ec9b0;margin:1.4rem 0 .3rem}
h3{font-size:.95rem;color:#4ec9b0;margin:1rem 0 .3rem}
.item{border:1px solid #333;border-radius:6px;margin:.6rem 0;background:#252526}
summary{cursor:pointer;padding:.5rem .8rem;font-weight:600}
.path{color:#c586c0;padding:.2rem .8rem;font-size:.85rem}
pre{margin:0;padding:.4rem .8rem;overflow-x:auto;font-size:.82rem;line-height:1.35}
.ctx{color:#888}.del{color:#f48771;background:#3a1d1d}.add{color:#89d185;background:#1d3a23}
.empty{color:#888;padding:1rem .8rem}
table{border-collapse:collapse;margin:.4rem 0}td{padding:.15rem .8rem;vertical-align:top}
.ok{color:#89d185}.warn{color:#e2c08d}.err{color:#f48771}
</style></head><body>`)
	b.WriteString("<h1>futils deploy report</h1>")

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
			b.WriteString("<h2>" + heading + "</h2><table>")
			for _, r := range rs {
				cls, mark, detail := "ok", "✓", r.Action.String()
				switch {
				case r.Err != nil:
					cls, mark, detail = "err", "✗", r.Err.Error()
				case r.Warning != "":
					cls, mark, detail = "warn", "⚠", r.Warning
				}
				b.WriteString(`<tr><td class="` + cls + `">` + mark + `</td><td>` +
					html.EscapeString(r.Type+"  "+r.Name) + `</td><td class="` + cls + `">` +
					html.EscapeString(detail) + `</td></tr>`)
			}
			b.WriteString("</table>")
		}
		renderRows("Deployed items", deployed)
		renderRows("Deleted items", deleted)
	}

	b.WriteString("<h2>Content diffs (deployed → local)</h2>")
	changed := 0
	for _, g := range groups {
		changed += len(g.Diffs)
	}
	b.WriteString(fmt.Sprintf(`<p style="color:#9cdcfe">%d changed item(s) — click to expand</p>`, changed))
	for _, g := range groups {
		if len(g.Diffs) == 0 {
			continue
		}
		b.WriteString("<h3>" + html.EscapeString(g.Target.DisplayName) + "</h3>")
		for _, it := range g.Diffs {
			b.WriteString(`<details class="item"><summary>` +
				html.EscapeString(it.Type+"  "+it.Name) + "</summary>")
			for _, p := range it.Parts {
				badge := ""
				if isJSON(p.Old) || isJSON(p.New) {
					badge = ` <span class="badge">json · prettified</span>`
				}
				b.WriteString(`<div class="path">` + html.EscapeString(p.Path) + badge + "</div><pre>")
				for _, ln := range cappedLineDiff(prettyForDiff(p.Old), prettyForDiff(p.New)) {
					cls, prefix := "ctx", " "
					switch ln.Op {
					case '-':
						cls, prefix = "del", "-"
					case '+':
						cls, prefix = "add", "+"
					}
					b.WriteString(`<span class="` + cls + `">` + prefix + " " + html.EscapeString(ln.Text) + "</span>\n")
				}
				b.WriteString("</pre>")
			}
			b.WriteString("</details>")
		}
	}
	if changed == 0 {
		b.WriteString(`<div class="empty">No changed items to diff.</div>`)
	}
	b.WriteString("</body></html>")
	return b.String()
}

// renderDeployDiffHTML is the compare-only view (no deploy results) used by the
// in-browser preview.
func renderDeployDiffHTML(groups []deployGroup) string {
	return renderDeployReport(groups, nil)
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

// showDiffsInBrowser renders the diff HTML to a temp file, opens it in the
// browser, and schedules the temp file for deletion (after the browser has had
// time to load it). The file lives in the OS temp dir — it is an ephemeral
// viewer, not a saved artifact.
func showDiffsInBrowser(groups []deployGroup) error {
	htmlDoc := renderDeployDiffHTML(groups)
	f, err := os.CreateTemp("", "futils-deploy-diff-*.html")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	path := filepath.FromSlash(f.Name())
	if _, err := f.WriteString(htmlDoc); err != nil {
		f.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	f.Close()
	if err := openInBrowser(path); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	// Delete after the browser has loaded it; if the process exits first, the OS
	// cleans the temp dir anyway.
	go func() {
		time.Sleep(5 * time.Second)
		_ = os.Remove(path)
	}()
	return nil
}
