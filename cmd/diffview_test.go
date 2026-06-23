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
	if !strings.Contains(got, " keep\n") || !strings.Contains(got, "-old\n") || !strings.Contains(got, "+new\n") || !strings.Contains(got, "+extra\n") {
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
	if !strings.Contains(html, "NB_Config") || !strings.Contains(html, "notebook-content.py") {
		t.Errorf("HTML missing item/part name")
	}
	if !strings.Contains(html, "DEV") || !strings.Contains(html, "TEST") {
		t.Errorf("HTML missing diff content")
	}
	// Raw content must be HTML-escaped (no live <script>).
	if strings.Contains(html, "<script>") {
		t.Errorf("content not HTML-escaped — raw <script> present")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped <script>")
	}
}

func TestRenderDeployDiffHTMLEmpty(t *testing.T) {
	html := renderDeployDiffHTML([]deployGroup{{}})
	if !strings.Contains(html, "<html") {
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
	// Items must NOT be open by default — the new markup emits `<details class="item changed">`,
	// so guard against the variant the code could wrongly produce.
	if strings.Contains(out, `<details class="item changed" open`) {
		t.Errorf("items should be collapsed by default (no open attribute)")
	}
	if !strings.Contains(out, "<details") {
		t.Errorf("expected <details> sections")
	}
	// A count header summarizing the number of changed items.
	if !strings.Contains(out, "2") {
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
		// NB_Config must be a deployed result so its diff renders under the deployed-only [6] gate.
		{Name: "NB_Config", Type: "Notebook", Action: deploy.ActionUpdate},
	}
	out := renderDeployReport(groups, results)

	if !strings.Contains(out, "<!doctype html>") {
		t.Errorf("expected a doctype")
	}
	// Outcomes present and labelled.
	if !strings.Contains(out, "NB_OK") || !strings.Contains(out, "NB_Warn") || !strings.Contains(out, "NB_Err") {
		t.Errorf("results section missing item names")
	}
	if !strings.Contains(out, "description not synced") {
		t.Errorf("warning text missing")
	}
	// Outcome markers and the action label must render (guards the results section).
	if !strings.Contains(out, "✓") || !strings.Contains(out, "⚠") || !strings.Contains(out, "✗") {
		t.Errorf("outcome markers (✓/⚠/✗) missing")
	}
	if !strings.Contains(out, results[0].Action.String()) {
		t.Errorf("action label %q missing for happy-path row", results[0].Action.String())
	}
	// Error text is HTML-escaped (no raw <x>).
	if strings.Contains(out, "boom <x>") || !strings.Contains(out, "boom &lt;x&gt;") {
		t.Errorf("error text not HTML-escaped")
	}
	// Compare section still rendered.
	if !strings.Contains(out, "NB_Config") {
		t.Errorf("compare section missing")
	}
}

func TestRenderDeployReportHasCardsAndCollapse(t *testing.T) {
	groups := []deployGroup{{Target: fabric.Workspace{DisplayName: "WS"}, Diffs: []ItemDiff{
		{Name: "NB_A", Type: "Notebook", Parts: []deploy.PartDiff{{Path: "c.py", Old: "a", New: "b"}}},
	}}}
	results := []deploy.Result{{Name: "NB_A", Type: "Notebook", Action: deploy.ActionUpdate}}
	out := renderDeployReport(groups, results)
	if !strings.Contains(out, `class="cards"`) {
		t.Error("report must include the summary cards")
	}
	if strings.Contains(out, `<details class="item changed" open`) || strings.Contains(out, `<details class="item" open`) {
		t.Error("diff items must be collapsed by default (no open attribute)")
	}
	// mockup uses inline onclick, not a <script> tag; both expand AND collapse buttons required
	if !strings.Contains(out, "Expand all") || !strings.Contains(out, "d.open=true") ||
		!strings.Contains(out, "Collapse all") {
		t.Error("expand/collapse-all controls + inline onclick expected (both Expand all and Collapse all must be present)")
	}
}

func TestRenderDeployReportDeleteOnlyHasNoContentDiffs(t *testing.T) {
	// A delete-only run: results are all ActionDelete; the compare still has a
	// Changed item's diff, but the report must NOT show it (fix [6]).
	groups := []deployGroup{{Target: fabric.Workspace{DisplayName: "WS"}, Diffs: []ItemDiff{
		{Name: "NB_Changed", Type: "Notebook", Parts: []deploy.PartDiff{{Path: "c.py", Old: "a", New: "b"}}},
	}}}
	results := []deploy.Result{{Name: "NB_Gone", Type: "Notebook", Action: deploy.ActionDelete}}
	out := renderDeployReport(groups, results)
	if strings.Contains(out, "NB_Changed") {
		t.Error("delete-only run must not render content diffs for items that were not deployed")
	}
}

func TestPrettyForDiff(t *testing.T) {
	min := `{"a":1,"b":{"c":2}}`
	got, wasJSON := prettyForDiff(min)
	if !strings.Contains(got, "\n") || !strings.Contains(got, "  \"a\": 1") {
		t.Errorf("minified JSON should be indented, got:\n%s", got)
	}
	if !wasJSON {
		t.Errorf("JSON input should report wasJSON=true")
	}
	verbatim, wasJSON2 := prettyForDiff("x=1\ny=2")
	if verbatim != "x=1\ny=2" {
		t.Errorf("non-JSON must be returned verbatim")
	}
	if wasJSON2 {
		t.Errorf("non-JSON input should report wasJSON=false")
	}
	// Whitespace-only difference collapses to identical output (no diff noise).
	pretty1, _ := prettyForDiff(`{"a":1}`)
	pretty2, _ := prettyForDiff(`{ "a" : 1 }`)
	if pretty1 != pretty2 {
		t.Errorf("formatting-only JSON differences must normalize equal")
	}
}

func TestRenderSummaryCardsOutcome(t *testing.T) {
	results := []deploy.Result{
		{Action: deploy.ActionCreate},
		{Action: deploy.ActionUpdate},
		{Action: deploy.ActionDelete},
		{Action: deploy.ActionDelete},
		{Action: deploy.ActionUpdate, Err: fmt.Errorf("boom")},
	}
	out := renderSummaryCards(nil, results)
	for _, want := range []string{">1<", "Created", ">2<", "Deleted", "Failed", `class="card`} {
		if !strings.Contains(out, want) {
			t.Errorf("outcome cards missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderSummaryCardsClassification(t *testing.T) {
	groups := []deployGroup{{Rows: []deploy.CompareRow{
		{Class: deploy.ClassNew}, {Class: deploy.ClassNew}, {Class: deploy.ClassChanged}, {Class: deploy.ClassOrphan},
	}}}
	out := renderSummaryCards(groups, nil)
	if !strings.Contains(out, "New") || !strings.Contains(out, "Changed") || !strings.Contains(out, "Orphan") {
		t.Errorf("classification cards missing labels in:\n%s", out)
	}
}

func TestRenderDeployReportGatesDiffByTypeAndName(t *testing.T) {
	// Only a DataPipeline named "X" was deployed. A Notebook named "X" was NOT deployed.
	// The Notebook's diff must NOT render, even though the name "X" matches — the gate
	// must key by type+name, not name alone.
	results := []deploy.Result{
		{Name: "X", Type: "DataPipeline", Action: deploy.ActionUpdate},
	}
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "WS"},
		Diffs: []ItemDiff{{
			Name: "X", Type: "Notebook",
			Parts: []deploy.PartDiff{{Path: "notebook-only-marker.py", Old: "a", New: "b"}},
		}},
	}}
	out := renderDeployReport(groups, results)
	if strings.Contains(out, "notebook-only-marker.py") {
		t.Error("Notebook diff for type=Notebook name=X must not render when only type=DataPipeline name=X was deployed (gate must key by type+name)")
	}
}

func TestRenderDeployReportPerWorkspaceHeadings(t *testing.T) {
	// Multi-group case: two groups with distinct workspace names, each with one
	// diff that is in the deployed set — must emit per-workspace headings.
	groups := []deployGroup{
		{
			Target: fabric.Workspace{DisplayName: "WS Alpha"},
			Diffs: []ItemDiff{{
				Name: "NB_Alpha", Type: "Notebook",
				Parts: []deploy.PartDiff{{Path: "p.py", Old: "a", New: "b"}},
			}},
		},
		{
			Target: fabric.Workspace{DisplayName: "WS Beta"},
			Diffs: []ItemDiff{{
				Name: "NB_Beta", Type: "Notebook",
				Parts: []deploy.PartDiff{{Path: "p.py", Old: "c", New: "d"}},
			}},
		},
	}
	results := []deploy.Result{
		{Name: "NB_Alpha", Type: "Notebook", Action: deploy.ActionUpdate},
		{Name: "NB_Beta", Type: "Notebook", Action: deploy.ActionUpdate},
	}
	out := renderDeployReport(groups, results)
	if !strings.Contains(out, "WS Alpha") {
		t.Error("multi-group report must include workspace heading for WS Alpha")
	}
	if !strings.Contains(out, "WS Beta") {
		t.Error("multi-group report must include workspace heading for WS Beta")
	}
	if !strings.Contains(out, `class="wsgroup"`) {
		t.Error("multi-group report must include the wsgroup class for workspace headings")
	}

	// Single-group case: exactly one group renders diffs — must NOT emit wsgroup heading.
	singleGroups := []deployGroup{
		{
			Target: fabric.Workspace{DisplayName: "WS Sole"},
			Diffs: []ItemDiff{{
				Name: "NB_Sole", Type: "Notebook",
				Parts: []deploy.PartDiff{{Path: "p.py", Old: "x", New: "y"}},
			}},
		},
	}
	singleResults := []deploy.Result{
		{Name: "NB_Sole", Type: "Notebook", Action: deploy.ActionUpdate},
	}
	singleOut := renderDeployReport(singleGroups, singleResults)
	if strings.Contains(singleOut, `class="wsgroup"`) {
		t.Error("single-group report must NOT include wsgroup element")
	}
}
