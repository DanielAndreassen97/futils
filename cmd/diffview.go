package cmd

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

// renderDeployDiffHTML builds a self-contained HTML report of every Changed
// item's per-part content diff (old=deployed, new=local). All file content is
// HTML-escaped. Returns a full <html> document.
func renderDeployDiffHTML(groups []deployGroup) string {
	var b strings.Builder
	b.WriteString(`<html><head><meta charset="utf-8"><title>futils deploy diff</title><style>
body{font-family:-apple-system,Menlo,monospace;background:#1e1e1e;color:#ddd;margin:0;padding:1.5rem}
h1{font-size:1.1rem;color:#9cdcfe}h2{font-size:1rem;color:#4ec9b0;margin:1.4rem 0 .3rem}
.item{border:1px solid #333;border-radius:6px;margin:.6rem 0;background:#252526}
summary{cursor:pointer;padding:.5rem .8rem;font-weight:600}
.path{color:#c586c0;padding:.2rem .8rem;font-size:.85rem}
pre{margin:0;padding:.4rem .8rem;overflow-x:auto;font-size:.82rem;line-height:1.35}
.ctx{color:#888}.del{color:#f48771;background:#3a1d1d}.add{color:#89d185;background:#1d3a23}
.empty{color:#888;padding:1rem .8rem}
</style></head><body>`)
	b.WriteString("<h1>futils deploy — content diffs (deployed → local)</h1>")
	changed := 0
	for _, g := range groups {
		changed += len(g.Diffs)
	}
	b.WriteString(fmt.Sprintf(`<p style="color:#9cdcfe">%d changed item(s) — click to expand</p>`, changed))
	for _, g := range groups {
		if len(g.Diffs) == 0 {
			continue
		}
		b.WriteString("<h2>" + html.EscapeString(g.Target.DisplayName) + "</h2>")
		for _, it := range g.Diffs {
			b.WriteString(`<details class="item"><summary>` +
				html.EscapeString(it.Type+"  "+it.Name) + "</summary>")
			for _, p := range it.Parts {
				b.WriteString(`<div class="path">` + html.EscapeString(p.Path) + "</div><pre>")
				for _, ln := range unifiedLineDiff(p.Old, p.New) {
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
