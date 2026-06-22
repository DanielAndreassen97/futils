package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestUnifiedLineDiffChangedLine(t *testing.T) {
	old := "a\nGUID-DEV\nc"
	new := "a\nGUID-TEST\nc"
	lines := unifiedLineDiff(old, new)
	// Expect: " a", "-GUID-DEV", "+GUID-TEST", " c"
	var minus, plus, ctx int
	for _, l := range lines {
		switch l.Op {
		case '-':
			minus++
			if l.Text != "GUID-DEV" {
				t.Errorf("unexpected removed line %q", l.Text)
			}
		case '+':
			plus++
			if l.Text != "GUID-TEST" {
				t.Errorf("unexpected added line %q", l.Text)
			}
		case ' ':
			ctx++
		}
	}
	if minus != 1 || plus != 1 || ctx != 2 {
		t.Fatalf("diff counts: -%d +%d ctx%d, want -1 +1 ctx2 (%#v)", minus, plus, ctx, lines)
	}
}

func TestUnifiedLineDiffIdentical(t *testing.T) {
	lines := unifiedLineDiff("x\ny", "x\ny")
	for _, l := range lines {
		if l.Op != ' ' {
			t.Errorf("identical input produced a non-context line: %#v", l)
		}
	}
}

func TestUnifiedLineDiffAddedAndRemoved(t *testing.T) {
	lines := unifiedLineDiff("keep\nold", "keep\nnew\nextra")
	var got string
	for _, l := range lines {
		got += string(l.Op) + l.Text + "\n"
	}
	// "keep" is common; "old" removed; "new" and "extra" added (order: removals before additions at a divergence is acceptable).
	if !contains(got, " keep\n") || !contains(got, "-old\n") || !contains(got, "+new\n") || !contains(got, "+extra\n") {
		t.Fatalf("diff missing expected lines:\n%s", got)
	}
}

func TestRenderDeployDiffHTMLEscapesAndIncludesItems(t *testing.T) {
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "DP - TEST - Config"},
		Diffs: []ItemDiff{{
			Name: "NB_Config", Type: "Notebook",
			Parts: []deploy.PartDiff{{Path: "notebook-content.py", Old: "lh = \"DEV\"\n<script>", New: "lh = \"TEST\"\n<script>"}},
		}},
	}}
	html := renderDeployDiffHTML(groups)
	if !contains(html, "NB_Config") || !contains(html, "notebook-content.py") {
		t.Errorf("HTML missing item/part name")
	}
	if !contains(html, "DEV") || !contains(html, "TEST") {
		t.Errorf("HTML missing diff content")
	}
	// Raw content must be HTML-escaped (no live <script>).
	if contains(html, "<script>") {
		t.Errorf("content not HTML-escaped — raw <script> present")
	}
	if !contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped <script>")
	}
}

func TestRenderDeployDiffHTMLEmpty(t *testing.T) {
	html := renderDeployDiffHTML([]deployGroup{{}})
	if !contains(html, "<html") {
		t.Errorf("expected a valid HTML doc even with no diffs")
	}
}

func TestRenderDeployDiffHTMLCollapsedByDefault(t *testing.T) {
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "W"},
		Diffs: []ItemDiff{
			{Name: "NB_A", Type: "Notebook", Parts: []deploy.PartDiff{{Path: "p", Old: "a", New: "b"}}},
			{Name: "NB_B", Type: "Notebook", Parts: []deploy.PartDiff{{Path: "p", Old: "c", New: "d"}}},
		},
	}}
	out := renderDeployDiffHTML(groups)
	// Items must NOT be open by default.
	if contains(out, "<details class=\"item\" open>") {
		t.Errorf("items should be collapsed by default (no open attribute)")
	}
	if !contains(out, "<details") {
		t.Errorf("expected <details> sections")
	}
	// A count header summarizing the number of changed items.
	if !contains(out, "2") {
		t.Errorf("expected a count of changed items in the header")
	}
}

func TestCappedLineDiffSkipsHugeInput(t *testing.T) {
	huge := strings.Repeat("x\n", maxDiffLines+50)
	got := cappedLineDiff(huge, huge+"y\n")
	if len(got) != 1 {
		t.Fatalf("an over-cap diff must collapse to one summary line, got %d lines", len(got))
	}
	if !strings.Contains(got[0].Text, "too large to diff inline") {
		t.Errorf("expected the cap summary, got %q", got[0].Text)
	}
}

func TestCappedLineDiffNormalInputStillDiffs(t *testing.T) {
	got := cappedLineDiff("a\nb\nc", "a\nB\nc")
	for _, l := range got {
		if strings.Contains(l.Text, "too large") {
			t.Fatalf("a normal-size diff must produce a real diff, not the cap: %+v", got)
		}
	}
	// Sanity: the changed line surfaces.
	var sawChange bool
	for _, l := range got {
		if l.Op == '-' && l.Text == "b" {
			sawChange = true
		}
	}
	if !sawChange {
		t.Errorf("expected the changed line in the diff, got %+v", got)
	}
}

func TestRenderDeployReportIncludesResults(t *testing.T) {
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "DP - TEST - Config"},
		Diffs: []ItemDiff{{
			Name: "NB_Config", Type: "Notebook",
			Parts: []deploy.PartDiff{{Path: "notebook-content.py", Old: "a", New: "b"}},
		}},
	}}
	results := []deploy.Result{
		{Name: "NB_OK", Type: "Notebook", Action: deploy.ActionCreate},
		{Name: "NB_Warn", Type: "Notebook", Action: deploy.ActionUpdate, Warning: "description not synced"},
		{Name: "NB_Err", Type: "Report", Action: deploy.ActionCreate, Err: fmt.Errorf("boom <x>")},
	}
	out := renderDeployReport(groups, results)

	if !contains(out, "<!doctype html>") {
		t.Errorf("expected a doctype")
	}
	// Outcomes present and labelled.
	if !contains(out, "NB_OK") || !contains(out, "NB_Warn") || !contains(out, "NB_Err") {
		t.Errorf("results section missing item names")
	}
	if !contains(out, "description not synced") {
		t.Errorf("warning text missing")
	}
	// Outcome markers and the action label must render (guards the results section).
	if !contains(out, "✓") || !contains(out, "⚠") || !contains(out, "✗") {
		t.Errorf("outcome markers (✓/⚠/✗) missing")
	}
	if !contains(out, results[0].Action.String()) {
		t.Errorf("action label %q missing for happy-path row", results[0].Action.String())
	}
	// Error text is HTML-escaped (no raw <x>).
	if contains(out, "boom <x>") || !contains(out, "boom &lt;x&gt;") {
		t.Errorf("error text not HTML-escaped")
	}
	// Compare section still rendered.
	if !contains(out, "NB_Config") {
		t.Errorf("compare section missing")
	}
}

func TestPrettyForDiff(t *testing.T) {
	min := `{"a":1,"b":{"c":2}}`
	got := prettyForDiff(min)
	if !strings.Contains(got, "\n") || !strings.Contains(got, "  \"a\": 1") {
		t.Errorf("minified JSON should be indented, got:\n%s", got)
	}
	if prettyForDiff("x=1\ny=2") != "x=1\ny=2" {
		t.Errorf("non-JSON must be returned verbatim")
	}
	// Whitespace-only difference collapses to identical output (no diff noise).
	if prettyForDiff(`{"a":1}`) != prettyForDiff(`{ "a" : 1 }`) {
		t.Errorf("formatting-only JSON differences must normalize equal")
	}
	if !isJSON(`{"a":1}`) || isJSON("x=1") {
		t.Errorf("isJSON misclassified")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (stringIndex(s, sub) >= 0) }
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
