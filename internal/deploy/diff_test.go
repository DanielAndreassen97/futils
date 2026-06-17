package deploy

import (
	"encoding/base64"
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
