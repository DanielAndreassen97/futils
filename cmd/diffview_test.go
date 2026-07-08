package cmd

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

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

func TestCommonAffixLen(t *testing.T) {
	// Shared "a" head and "c" tail; only the middle differs.
	prefix, suffix := commonAffixLen([]string{"a", "X", "c"}, []string{"a", "Y", "c"})
	if prefix != 1 || suffix != 1 {
		t.Fatalf("prefix/suffix = %d/%d, want 1/1", prefix, suffix)
	}
	// Identical slices: prefix consumes everything, suffix must not double-count.
	prefix, suffix = commonAffixLen([]string{"x", "y"}, []string{"x", "y"})
	if prefix != 2 || suffix != 0 {
		t.Fatalf("identical: prefix/suffix = %d/%d, want 2/0 (suffix must not overlap prefix)", prefix, suffix)
	}
	// Nothing in common.
	prefix, suffix = commonAffixLen([]string{"a"}, []string{"b"})
	if prefix != 0 || suffix != 0 {
		t.Fatalf("disjoint: prefix/suffix = %d/%d, want 0/0", prefix, suffix)
	}
}

func TestCappedLineDiffSkipsHugeDivergentInput(t *testing.T) {
	// Two large, fully DIFFERENT blobs → the divergent core's AREA (core²) exceeds
	// the cap. sqrt(maxDiffCells) lines each side overshoots the cell budget.
	side := int(math.Sqrt(float64(maxDiffCells))) + 100
	old := strings.Repeat("a\n", side)
	new := strings.Repeat("b\n", side)
	got := cappedLineDiff(old, new)
	if len(got) != 1 {
		t.Fatalf("an over-cap diff must collapse to one summary line, got %d lines", len(got))
	}
	if !strings.Contains(got[0].Text, "too large to diff inline") {
		t.Errorf("expected the cap summary, got %q", got[0].Text)
	}
}

func TestCappedLineDiffBoundsLopsidedDiff(t *testing.T) {
	// A wholesale-new part (old empty) has a 1×N core, so the AREA cap never
	// trips — but folding can't collapse '+' lines, so without a rendered-lines
	// bound every added line becomes a <span> (the minified-JSON-pretty-printed
	// case the old maxDiffLines guard existed for). The folded output must be
	// truncated with a marker, not emitted in full.
	var sb strings.Builder
	for i := 0; i < maxRenderedDiffLines+50_000; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	got := cappedLineDiff("", sb.String())
	if len(got) > maxRenderedDiffLines+1 {
		t.Fatalf("lopsided diff must be truncated to maxRenderedDiffLines(+marker), got %d lines", len(got))
	}
	last := got[len(got)-1]
	if last.Op != '@' || !strings.Contains(last.Text, "truncated") {
		t.Errorf("truncated diff must end with a truncation marker, got op=%q text=%q", last.Op, last.Text)
	}
}

func TestCappedLineDiffBigFileSmallChangeStillDiffs(t *testing.T) {
	// A big file with a single changed line MUST produce a real diff — the cap is
	// on the divergent core's area, not total file size. Prefix/suffix trim leaves
	// a 1-line core here, so this is the easy case; the scattered-edit test above
	// covers the harder large-core variant.
	var oldB, newB strings.Builder
	total := 3400
	for i := 0; i < total; i++ {
		line := fmt.Sprintf("line%d\n", i)
		oldB.WriteString(line)
		if i == total/2 {
			newB.WriteString("CHANGED\n")
		} else {
			newB.WriteString(line)
		}
	}
	got := cappedLineDiff(oldB.String(), newB.String())
	for _, l := range got {
		if strings.Contains(l.Text, "too large") {
			t.Fatalf("a big file with one changed line must diff, not cap: %q", l.Text)
		}
	}
	var sawRem, sawAdd bool
	for _, l := range got {
		if l.Op == '-' && l.Text == fmt.Sprintf("line%d", total/2) {
			sawRem = true
		}
		if l.Op == '+' && l.Text == "CHANGED" {
			sawAdd = true
		}
	}
	if !sawRem || !sawAdd {
		t.Fatalf("expected the changed line in the diff (rem=%v add=%v)", sawRem, sawAdd)
	}
}

func TestCappedLineDiffScatteredChangesLargeCoreStillDiffs(t *testing.T) {
	// The real bug: changes are SPREAD across a big file, so prefix/suffix trim
	// leaves a large divergent core (~2300 lines) even though only a handful of
	// lines actually differ. The product (core²) is still tiny vs the cap, so it
	// must diff — and fold the identical stretches between the scattered changes.
	const total = 3412
	var oldB, newB strings.Builder
	for i := 0; i < total; i++ {
		line := fmt.Sprintf("line%d\n", i)
		oldB.WriteString(line)
		switch i {
		case 100, 2400: // an early and a late edit → core spans ~2300 lines
			newB.WriteString(fmt.Sprintf("EDITED%d\n", i))
		default:
			newB.WriteString(line)
		}
	}
	got := cappedLineDiff(oldB.String(), newB.String())
	for _, l := range got {
		if strings.Contains(l.Text, "too large") {
			t.Fatalf("scattered edits with a small product must diff, not cap: %q", l.Text)
		}
	}
	var sawEarly, sawLate, folds int
	for _, l := range got {
		if l.Op == '+' && l.Text == "EDITED100" {
			sawEarly++
		}
		if l.Op == '+' && l.Text == "EDITED2400" {
			sawLate++
		}
		if l.Op == '@' {
			folds++
		}
	}
	if sawEarly == 0 || sawLate == 0 {
		t.Fatalf("both scattered edits must surface (early=%d late=%d)", sawEarly, sawLate)
	}
	if folds == 0 {
		t.Error("the long identical stretch between the two edits must fold")
	}
}

func TestFoldContextCollapsesLongRuns(t *testing.T) {
	// One change surrounded by lots of context: distant context folds into a
	// marker, near context (±contextLines) is kept.
	var lines []DiffLine
	for i := 0; i < 50; i++ {
		lines = append(lines, DiffLine{' ', fmt.Sprintf("ctx%d", i)})
	}
	lines[25] = DiffLine{'-', "before"}
	lines = append(lines[:26], append([]DiffLine{{'+', "after"}}, lines[26:]...)...)

	got := foldContext(lines, contextLines)

	var folds, ctx int
	for _, l := range got {
		switch l.Op {
		case '@':
			folds++
		case ' ':
			ctx++
		}
	}
	if folds == 0 {
		t.Fatal("expected at least one fold marker collapsing distant context")
	}
	// Near the single change we keep ~contextLines on each side, plus a little
	// slack — never the full 50 lines.
	if ctx > 4*contextLines {
		t.Fatalf("context not folded: kept %d context lines, want <= %d", ctx, 4*contextLines)
	}
	// The change itself must survive folding.
	var sawChange bool
	for _, l := range got {
		if l.Op == '-' && l.Text == "before" {
			sawChange = true
		}
	}
	if !sawChange {
		t.Error("fold must keep changed lines")
	}
}

func TestFoldContextNoChangeReturnsAsIs(t *testing.T) {
	lines := []DiffLine{{' ', "a"}, {' ', "b"}}
	got := foldContext(lines, contextLines)
	if len(got) != 2 {
		t.Fatalf("all-context input must pass through unfolded, got %d lines", len(got))
	}
}

func TestRenderDeployReportBigFileSmallChangeShowsDiff(t *testing.T) {
	// End-to-end: a Changed item whose part is a big file with a tiny change must
	// render the actual changed token, not the cap message.
	var oldB, newB strings.Builder
	for i := 0; i < 3400; i++ {
		oldB.WriteString(fmt.Sprintf("row%d\n", i))
		if i == 100 {
			newB.WriteString("lh = \"TEST_LAKEHOUSE\"\n")
		} else {
			newB.WriteString(fmt.Sprintf("row%d\n", i))
		}
	}
	groups := []deployGroup{{
		Target: fabric.Workspace{DisplayName: "WS"},
		Diffs: []ItemDiff{{
			Name: "NB_Big", Type: "Notebook",
			Parts: []deploy.PartDiff{{Path: "notebook-content.py", Old: oldB.String(), New: newB.String()}},
		}},
	}}
	out := renderDeployReport(groups, nil, nil)
	if strings.Contains(out, "too large to diff") {
		t.Error("big file with a tiny change must render a real diff, not the cap message")
	}
	if !strings.Contains(out, "TEST_LAKEHOUSE") {
		t.Error("the actual changed token must appear in the rendered diff")
	}
	if !strings.Contains(out, "unchanged lines") {
		t.Error("expected a folded-context marker for the long unchanged runs")
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
	out := renderDeployReport(groups, results, nil)

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
	out := renderDeployReport(groups, results, nil)
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
	out := renderDeployReport(groups, results, nil)
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
	out := renderDeployReport(groups, results, nil)
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
	out := renderDeployReport(groups, results, nil)
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
	singleOut := renderDeployReport(singleGroups, singleResults, nil)
	if strings.Contains(singleOut, `class="wsgroup"`) {
		t.Error("single-group report must NOT include wsgroup element")
	}
}

func TestRenderDeployReportPostDeploySection(t *testing.T) {
	results := []deploy.Result{{Name: "NB_A", Type: "Notebook", Action: deploy.ActionUpdate, ID: "id-a", WorkspaceID: "ws-1"}}
	postRuns := []postDeployOutcome{
		{Run: postDeployRun{Name: "NB_A", WorkspaceName: "WS One"}, Status: fabric.JobStatusCompleted, Duration: 14 * time.Second},
		{Run: postDeployRun{Name: "NB_B", WorkspaceName: "WS One"}, Status: fabric.JobStatusFailed, Err: errors.New("job Failed: boom")},
		{Run: postDeployRun{Name: "NB_C", WorkspaceName: "WS One"}, Status: postDeployStatusSkipped},
	}
	html := renderDeployReport(nil, results, postRuns)
	for _, want := range []string{"Post-deploy runs", "NB_A", "Completed in 14s", "job Failed: boom", "skipped — earlier run failed"} {
		if !strings.Contains(html, want) {
			t.Fatalf("report missing %q", want)
		}
	}
	// No section when nothing ran.
	if strings.Contains(renderDeployReport(nil, results, nil), "Post-deploy runs") {
		t.Fatal("report must omit the Post-deploy runs section when no runs happened")
	}
}
