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
	parts, outcome, err := SubstituteParts(item, map[string]string{}, resolver, nil)
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
	parts, outcome, err := SubstituteParts(item, map[string]string{}, resolver, rb)
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
	_, outcome, err := SubstituteParts(item, map[string]string{}, resolver, rb)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].ItemName != "NB_Config" {
		t.Fatalf("unresolved = %#v (want one tagged with NB_Config)", outcome.Unresolved)
	}
}

func TestSubstitutePartsCollectsReportBindings(t *testing.T) {
	rb := newRebindFixture(t, nil)
	item := LocalItem{
		Type:        "Report",
		DisplayName: "Daniel - Testing",
		Parts:       []Part{{Path: "definition.pbir", Content: flatPBIR("DP - DEV - SemMod", "HR", devHRModel)}},
	}
	_, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb)
	if err != nil {
		t.Fatalf("SubstituteParts: %v", err)
	}
	if len(outcome.ReportBindings) != 1 || outcome.ReportBindings[0].Model != "HR" {
		t.Fatalf("ReportBindings not threaded, got %+v", outcome.ReportBindings)
	}
}
