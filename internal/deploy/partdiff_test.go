package deploy

import (
	"encoding/base64"
	"strings"
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

// TestDiffPartsPBIRSemanticBinding: Fabric round-trips our XMLA-style
// byConnection as a flat connectionString under another schema version — the
// two shapes bind the same model and must compare as unchanged, while a
// genuinely different model GUID must still diff.
func TestDiffPartsPBIRSemanticBinding(t *testing.T) {
	local := []byte(`{
	  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/report/definitionProperties/1.0.0/schema.json",
	  "datasetReference": {"byConnection": {
	    "connectionString": null,
	    "connectionType": "pbiServiceXmlaStyleLive",
	    "name": "EntityDataSource",
	    "pbiModelDatabaseName": "eeeeeeee-0000-0000-0000-000000000001",
	    "pbiModelVirtualServerName": "sobe_wowvirtualserver",
	    "pbiServiceModelId": null
	  }},
	  "version": "4.0"
	}`)
	flat := `{
	  "$schema": "https://developer.microsoft.com/json-schemas/fabric/item/report/definitionProperties/2.0.0/schema.json",
	  "datasetReference": {"byConnection": {
	    "connectionString": "Data Source=\"powerbi://api.powerbi.com/v1.0/myorg/DW - TEST - SemMod\";initial catalog=HR;integrated security=ClaimsToken;semanticmodelid=eeeeeeee-0000-0000-0000-000000000001"
	  }},
	  "version": "4.0"
	}`
	deployed := &fabric.Definition{Parts: []fabric.DefinitionPart{{
		Path: "definition.pbir", Payload: base64.StdEncoding.EncodeToString([]byte(flat)), PayloadType: "InlineBase64",
	}}}
	if diffs := DiffParts(map[string][]byte{"definition.pbir": local}, deployed); len(diffs) != 0 {
		t.Fatalf("same model GUID must compare unchanged across shapes, got %+v", diffs)
	}

	otherModel := []byte(strings.ReplaceAll(string(local), "eeeeeeee-0000-0000-0000-000000000001", "eeeeeeee-0000-0000-0000-000000000002"))
	diffs := DiffParts(map[string][]byte{"definition.pbir": otherModel}, deployed)
	if len(diffs) != 1 {
		t.Fatalf("different model GUID must diff, got %+v", diffs)
	}
}
