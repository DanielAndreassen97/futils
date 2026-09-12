package deploy

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestNormalizePartJSONReorderEqual(t *testing.T) {
	a := normalizePart([]byte(`{"b":2,"a":1}`))
	b := normalizePart([]byte(`{"a":1,"b":2}`))
	if string(a) != string(b) {
		t.Errorf("reordered JSON should normalize equal: %q vs %q", a, b)
	}
}

// TestNormalizePartPreservesLargeIntegers proves JSON canonicalization keeps
// integer precision above 2^53: two parts differing only in such a value must
// NOT normalize identically (float64 round-tripping would collapse them and
// silently classify the edit as Unchanged).
func TestNormalizePartPreservesLargeIntegers(t *testing.T) {
	a := normalizePart([]byte(`{"id": 9007199254740993}`))
	b := normalizePart([]byte(`{"id": 9007199254740992}`))
	if string(a) == string(b) {
		t.Fatalf("distinct large integers must not normalize equal: %q", a)
	}
}

func TestNormalizePartWhitespace(t *testing.T) {
	a := normalizePart([]byte("line1   \r\nline2\t\n"))
	b := normalizePart([]byte("line1\nline2"))
	if string(a) != string(b) {
		t.Errorf("whitespace should normalize equal: %q vs %q", a, b)
	}
}

func deployedDef(parts map[string]string) *fabric.Definition {
	d := &fabric.Definition{}
	for path, content := range parts {
		d.Parts = append(d.Parts, fabric.DefinitionPart{
			Path:        path,
			Payload:     base64.StdEncoding.EncodeToString([]byte(content)),
			PayloadType: "InlineBase64",
		})
	}
	return d
}

func TestDiffPartsUnchanged(t *testing.T) {
	local := map[string][]byte{"f.json": []byte(`{"a":1,"b":2}`)}
	deployed := deployedDef(map[string]string{"f.json": `{"b":2,"a":1}`}) // reordered, same
	if len(DiffParts(local, deployed)) != 0 {
		t.Error("reordered-but-equal JSON should be unchanged")
	}
}

func TestDiffPartsContentDiff(t *testing.T) {
	local := map[string][]byte{"f.py": []byte("x=1")}
	deployed := deployedDef(map[string]string{"f.py": "x=2"})
	if len(DiffParts(local, deployed)) == 0 {
		t.Error("different content should be changed")
	}
}

func TestDiffPartsDifferentPartSet(t *testing.T) {
	local := map[string][]byte{"a.py": []byte("x")}
	deployed := deployedDef(map[string]string{"a.py": "x", "b.py": "y"})
	if len(DiffParts(local, deployed)) == 0 {
		t.Error("extra deployed part should be changed")
	}
}

// Fabric's getDefinition returns a .platform part, but DiscoverItems excludes
// .platform from local parts — so a deployed-only .platform must NOT be read as
// a content change, or every existing item is falsely flagged Changed.
func TestDiffPartsIgnoresDeployedPlatformFull(t *testing.T) {
	local := map[string][]byte{"notebook-content.py": []byte("x=1")}
	deployed := deployedDef(map[string]string{
		"notebook-content.py": "x=1",
		".platform":           `{"metadata":{"type":"Notebook","displayName":"NB","description":"d"}}`,
	})
	if len(DiffParts(local, deployed)) != 0 {
		t.Error("deployed-only .platform must not count as a content change")
	}
}

func TestDiffPartsIgnoresDeployedPlatform(t *testing.T) {
	local := map[string][]byte{"notebook-content.py": []byte("x=1")}
	deployed := deployedDef(map[string]string{
		"notebook-content.py": "x=1",
		".platform":           `{"metadata":{"type":"Notebook","displayName":"NB"}}`,
	})
	if diffs := DiffParts(local, deployed); len(diffs) != 0 {
		t.Errorf("expected no diffs (only deployed-only .platform), got %+v", diffs)
	}
}

func TestDeployedDescription(t *testing.T) {
	deployed := deployedDef(map[string]string{
		"notebook-content.py": "x=1",
		".platform":           `{"metadata":{"type":"Notebook","displayName":"NB","description":"Hello"}}`,
	})
	if got := DeployedDescription(deployed); got != "Hello" {
		t.Errorf("got %q, want %q", got, "Hello")
	}
}

func TestDeployedDescriptionNoPlatform(t *testing.T) {
	deployed := deployedDef(map[string]string{"notebook-content.py": "x=1"})
	if got := DeployedDescription(deployed); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSubstitutePartsNilRebinderIsNoOp(t *testing.T) {
	item := LocalItem{
		Type:        "Notebook",
		DisplayName: "NB_Config",
		Parts:       []Part{{Path: "notebook-content.py", Content: []byte("print(1)\n")}},
	}
	resolver := newResolverFixture()
	parts, outcome, err := SubstituteParts(item, map[string]string{}, resolver, nil, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("nil rebinder should yield no unresolved, got %#v", outcome.Unresolved)
	}
	if string(parts["notebook-content.py"]) != "print(1)\n" {
		t.Errorf("content changed under nil rebinder: %q", parts["notebook-content.py"])
	}
}

func TestSubstitutePartsAppliesRebindToNotebookPart(t *testing.T) {
	rb := newRebindFixture(t, nil)
	nb := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", devSilverLH)
	item := LocalItem{
		Type:        "Notebook",
		DisplayName: "NB_Config",
		Parts:       []Part{{Path: "notebook-content.py", Content: nb}},
	}
	resolver := newResolverFixture()
	parts, outcome, err := SubstituteParts(item, map[string]string{}, resolver, rb, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", outcome.Unresolved)
	}
	got := string(parts["notebook-content.py"])
	if !strings.Contains(got, "test-config-lh") || strings.Contains(got, devConfigLH) {
		t.Errorf("rebind not applied to notebook part:\n%s", got)
	}
	if len(outcome.Changes) == 0 {
		t.Errorf("expected rebind changes to be reported, got none")
	}
}

func TestSubstitutePartsTagsUnresolvedWithItemName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	unknown := "99999999-9999-9999-9999-999999999999"
	nb := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	item := LocalItem{Type: "Notebook", DisplayName: "NB_Config",
		Parts: []Part{{Path: "notebook-content.py", Content: nb}}}
	resolver := newResolverFixture()
	_, outcome, err := SubstituteParts(item, map[string]string{}, resolver, rb, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].ItemName != "NB_Config" {
		t.Fatalf("unresolved = %#v (want one tagged with NB_Config)", outcome.Unresolved)
	}
}

// TestReorderedOnlyDetectsMovedTMDLBlocks: a table whose columns merely sit in a
// different order is text that differs and meaning that does not. Fabric's
// serialiser chooses that order, so treating it as a change made every semantic
// model report Changed on every deploy, forever.
func TestReorderedOnlyDetectsMovedTMDLBlocks(t *testing.T) {
	deployed := strings.Join([]string{
		"table 'Fakta Fravaer'",
		"\tcolumn VaktkodeID",
		"\t\tdataType: int64",
		"",
		"\tcolumn StartKlokkeslett",
		"\t\tdataType: string",
	}, "\n")
	// Same six lines, the two column blocks swapped.
	local := strings.Join([]string{
		"table 'Fakta Fravaer'",
		"\tcolumn StartKlokkeslett",
		"\t\tdataType: string",
		"",
		"\tcolumn VaktkodeID",
		"\t\tdataType: int64",
	}, "\n")

	if !reorderedOnly("definition/tables/Fakta Fravaer.tmdl", deployed, local) {
		t.Error("a pure block move in TMDL must be recognised as a reorder")
	}
	// An edit inside the moved block is a real change, not a reorder.
	edited := strings.Replace(local, "dataType: int64", "dataType: string", 1)
	if reorderedOnly("definition/tables/Fakta Fravaer.tmdl", deployed, edited) {
		t.Error("an edited line must never read as a reorder")
	}
	// A removed line changes the count, so it cannot pass either.
	shorter := strings.Join(strings.Split(local, "\n")[:5], "\n")
	if reorderedOnly("definition/tables/Fakta Fravaer.tmdl", deployed, shorter) {
		t.Error("a removed line must never read as a reorder")
	}
	// Order matters outside TMDL: two swapped notebook cells are a real change.
	if reorderedOnly("notebook-content.py", deployed, local) {
		t.Error("reorder tolerance must be limited to .tmdl")
	}
	// A wholly new or wholly removed part is not a reorder.
	if reorderedOnly("definition/tables/x.tmdl", "", local) {
		t.Error("an added part must never read as a reorder")
	}
}

// DiffParts must flag the reordered part rather than drop it: the deploy verdict
// ignores it, but the report still has it to show.
func TestDiffPartsFlagsReorderedParts(t *testing.T) {
	oldText := "table T\n\tcolumn A\n\tcolumn B"
	newText := "table T\n\tcolumn B\n\tcolumn A"
	deployed := &fabric.Definition{Parts: []fabric.DefinitionPart{
		{Path: "definition/tables/T.tmdl", Payload: base64.StdEncoding.EncodeToString([]byte(oldText))},
	}}
	diffs := DiffParts(map[string][]byte{"definition/tables/T.tmdl": []byte(newText)}, deployed)
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	if !diffs[0].Reordered {
		t.Error("a reorder-only TMDL part must be flagged Reordered")
	}
	if diffs[0].Old == "" || diffs[0].New == "" {
		t.Error("the reordered part must keep both sides so the report can show the move")
	}
}

// --- TMDL canonicalization -------------------------------------------------
//
// Fabric stores a semantic model through the TMDL serializer, which picks one
// spelling out of several the language accepts. A hand-written .tmdl in git
// often uses another, equally valid one, so the two sides differ as text while
// meaning the same thing — and every deploy reports the model Changed forever.

// TestNormalizeTMDLFencedAndIndentedExpressionsMatch covers the backtick rule:
// a multi-line expression may be wrapped in ``` or just indented under the
// declaration. The serializer only emits the fence when the content would not
// survive the round trip otherwise, so an optional fence in git must not read
// as a difference.
func TestNormalizeTMDLFencedAndIndentedExpressionsMatch(t *testing.T) {
	fenced := "table T\n\n\tmeasure 'Antall kritiske' = ```\n\t\t\tCALCULATE(\n\t\t\t    [Antall],\n\t\t\t    KEEPFILTERS( 'Dim S'[Grad] = \"Critical\" )\n\t\t\t)\n\t\t\t```\n\t\tformatString: #,0\n"
	plain := "table T\n\n\tmeasure 'Antall kritiske' =\n\t\t\tCALCULATE(\n\t\t\t    [Antall],\n\t\t\t    KEEPFILTERS( 'Dim S'[Grad] = \"Critical\" )\n\t\t\t)\n\t\tformatString: #,0\n"
	a := normalizePartFor("definition/tables/T.tmdl", []byte(fenced))
	b := normalizePartFor("definition/tables/T.tmdl", []byte(plain))
	if string(a) != string(b) {
		t.Errorf("fenced and indented spellings must normalize equal:\n--- fenced ---\n%s\n--- plain ---\n%s", a, b)
	}
	if strings.Contains(string(a), "```") {
		t.Errorf("fence markers must be gone after normalization:\n%s", a)
	}
	// The expression itself is DAX and must survive intact, quotes included.
	if !strings.Contains(string(a), `KEEPFILTERS( 'Dim S'[Grad] = "Critical" )`) {
		t.Errorf("expression body was altered:\n%s", a)
	}
}

// TestNormalizeTMDLDropsOptionalNameQuotes covers the quoting rule: a name is
// only quoted when it contains a dot, equals, colon, single quote or
// whitespace. The serializer drops the quotes when they are optional, so git
// keeping them must not read as a difference — and a name that genuinely needs
// them must keep them.
func TestNormalizeTMDLDropsOptionalNameQuotes(t *testing.T) {
	quoted := "table T\n\n\tmeasure 'Compliance-andel' = DIVIDE( [A], [B] )\n\n\tmeasure 'Antall kritiske' = COUNTROWS( T )\n\n\tcolumn 'EnhetID'\n\t\tdataType: int64\n"
	bare := "table T\n\n\tmeasure Compliance-andel = DIVIDE( [A], [B] )\n\n\tmeasure 'Antall kritiske' = COUNTROWS( T )\n\n\tcolumn EnhetID\n\t\tdataType: int64\n"
	a := normalizePartFor("definition/tables/T.tmdl", []byte(quoted))
	b := normalizePartFor("definition/tables/T.tmdl", []byte(bare))
	if string(a) != string(b) {
		t.Errorf("optional quotes must normalize away:\n--- quoted ---\n%s\n--- bare ---\n%s", a, b)
	}
	if !strings.Contains(string(a), "measure 'Antall kritiske'") {
		t.Errorf("a name containing whitespace must keep its quotes:\n%s", a)
	}
}

// TestNormalizeTMDLLeavesExpressionBodiesAlone is the regression guard for the
// quoting rule: inside a DAX or M body, single quotes delimit table names and
// are not TMDL name syntax. Stripping them there would both corrupt the
// comparison and hide real changes.
func TestNormalizeTMDLLeavesExpressionBodiesAlone(t *testing.T) {
	src := "table T\n\n\tpartition P = m\n\t\tmode: import\n\t\tsource =\n\t\t\t\tlet\n\t\t\t\t    Source = Sql.Database(\"h\", \"lh_gold\"),\n\t\t\t\t    Nav = Source{[Schema=\"Dim\",Item=\"Bank\"]}[Data]\n\t\t\t\tin\n\t\t\t\t    Nav\n\n\tmeasure M =\n\t\t\tCALCULATE( [X], 'Dim Enhet'[Type] = \"A\" )\n"
	got := string(normalizePartFor("definition/tables/T.tmdl", []byte(src)))
	for _, want := range []string{`Sql.Database("h", "lh_gold")`, `Item="Bank"`, `'Dim Enhet'[Type]`} {
		if !strings.Contains(got, want) {
			t.Errorf("expression body altered, %q missing:\n%s", want, got)
		}
	}
}

// TestNormalizeTMDLOnlyAppliesToTMDL keeps the canonicalization scoped: a JSON
// part that happens to contain backticks is normalized as JSON, untouched.
func TestNormalizeTMDLOnlyAppliesToTMDL(t *testing.T) {
	src := []byte("measure 'X' = ```\n\tbody\n\t```\n")
	if got := string(normalizePartFor("pipeline-content.json", src)); !strings.Contains(got, "```") {
		t.Errorf("non-tmdl part must not be TMDL-canonicalized:\n%s", got)
	}
}
