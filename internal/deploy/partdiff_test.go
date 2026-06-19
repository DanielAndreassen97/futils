package deploy

import (
	"encoding/base64"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestDiffPartsReportsChangedPart(t *testing.T) {
	local := map[string][]byte{
		"notebook-content.py": []byte("x = 1\nlh = \"TEST-GUID\"\n"),
	}
	deployed := &fabric.Definition{Parts: []fabric.DefinitionPart{
		{Path: "notebook-content.py", Payload: base64.StdEncoding.EncodeToString([]byte("x = 1\nlh = \"DEV-GUID\"\n")), PayloadType: "InlineBase64"},
	}}
	diffs := DiffParts(local, deployed)
	if len(diffs) != 1 || diffs[0].Path != "notebook-content.py" {
		t.Fatalf("diffs = %#v", diffs)
	}
	if !contains(diffs[0].Old, "DEV-GUID") || !contains(diffs[0].New, "TEST-GUID") {
		t.Errorf("old/new not captured: %#v", diffs[0])
	}
}

func TestDiffPartsNoDiffWhenEqual(t *testing.T) {
	content := "same\n"
	local := map[string][]byte{"p": []byte(content)}
	deployed := &fabric.Definition{Parts: []fabric.DefinitionPart{
		{Path: "p", Payload: base64.StdEncoding.EncodeToString([]byte(content)), PayloadType: "InlineBase64"},
	}}
	if diffs := DiffParts(local, deployed); len(diffs) != 0 {
		t.Errorf("expected no diffs for equal content, got %#v", diffs)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
