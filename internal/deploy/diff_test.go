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

func TestPartsChangedUnchanged(t *testing.T) {
	local := map[string][]byte{"f.json": []byte(`{"a":1,"b":2}`)}
	deployed := deployedDef(map[string]string{"f.json": `{"b":2,"a":1}`}) // reordered, same
	if PartsChanged(local, deployed) {
		t.Error("reordered-but-equal JSON should be unchanged")
	}
}

func TestPartsChangedContentDiff(t *testing.T) {
	local := map[string][]byte{"f.py": []byte("x=1")}
	deployed := deployedDef(map[string]string{"f.py": "x=2"})
	if !PartsChanged(local, deployed) {
		t.Error("different content should be changed")
	}
}

func TestPartsChangedDifferentPartSet(t *testing.T) {
	local := map[string][]byte{"a.py": []byte("x")}
	deployed := deployedDef(map[string]string{"a.py": "x", "b.py": "y"})
	if !PartsChanged(local, deployed) {
		t.Error("extra deployed part should be changed")
	}
}

func TestSubstitutePartsNilRebinderIsNoOp(t *testing.T) {
	item := LocalItem{
		Type:        "Notebook",
		DisplayName: "NB_Config",
		Parts: []Part{{Path: "notebook-content.py", Content: []byte("print(1)\n")}},
	}
	resolver := newResolverFixture()
	parts, unresolved, err := SubstituteParts(item, "TEST", Parameters{}, map[string]string{}, resolver, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(unresolved) != 0 {
		t.Errorf("nil rebinder should yield no unresolved, got %#v", unresolved)
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
		Parts: []Part{{Path: "notebook-content.py", Content: nb}},
	}
	resolver := newResolverFixture()
	parts, unresolved, err := SubstituteParts(item, "TEST", Parameters{}, map[string]string{}, resolver, rb)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %#v", unresolved)
	}
	got := string(parts["notebook-content.py"])
	if !strings.Contains(got, "test-config-lh") || strings.Contains(got, devConfigLH) {
		t.Errorf("rebind not applied to notebook part:\n%s", got)
	}
}

func TestSubstitutePartsTagsUnresolvedWithItemName(t *testing.T) {
	rb := newRebindFixture(t, nil)
	unknown := "99999999-9999-9999-9999-999999999999"
	nb := rebindNotebook(devConfigLH, devConfigWS, "LH_ConfigLog", unknown)
	item := LocalItem{Type: "Notebook", DisplayName: "NB_Config",
		Parts: []Part{{Path: "notebook-content.py", Content: nb}}}
	resolver := newResolverFixture()
	_, unresolved, err := SubstituteParts(item, "TEST", Parameters{}, map[string]string{}, resolver, rb)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0].ItemName != "NB_Config" {
		t.Fatalf("unresolved = %#v (want one tagged with NB_Config)", unresolved)
	}
}
