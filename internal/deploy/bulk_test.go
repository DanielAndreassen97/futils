package deploy

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// bulkRecorder records the parts/opts/ws passed to BulkImportDefinitions and
// returns a canned result. Embeds fakeFabric for the other interface methods.
type bulkRecorder struct {
	fakeFabric
	gotWS    string
	gotParts []fabric.DefinitionPart
	gotOpts  fabric.BulkImportOptions
	result   *fabric.BulkImportResult
}

func (b *bulkRecorder) BulkImportDefinitions(token, ws string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error) {
	b.gotWS = ws
	b.gotParts = parts
	b.gotOpts = opts
	return b.result, nil
}

func TestBulkImportBuildsFlatPayload(t *testing.T) {
	item := LocalItem{
		Type:        "SemanticModel",
		DisplayName: "Sales",
		FolderPath:  "models/Sales.SemanticModel",
		Platform:    []byte(`{"metadata":{"type":"SemanticModel","displayName":"Sales"},"config":{"version":"2.0","logicalId":"GIT-LID"}}`),
		Parts:       []Part{{Path: "definition/model.tmdl", Content: []byte("model Sales")}},
	}
	rec := &bulkRecorder{result: &fabric.BulkImportResult{Details: []fabric.BulkImportDetail{
		{ItemID: "new-id", ItemDisplayName: "Sales", ItemType: "SemanticModel", OperationType: "Create", OperationStatus: "Succeeded"},
	}}}
	target := fabric.Workspace{ID: "ws-1", DisplayName: "Prod"}

	results, err := BulkImport(rec, "tok", target, []LocalItem{item}, nil)
	if err != nil {
		t.Fatalf("BulkImport: %v", err)
	}

	if rec.gotWS != "ws-1" {
		t.Errorf("workspace = %q, want ws-1", rec.gotWS)
	}
	if !rec.gotOpts.AllowPairingByName {
		t.Error("AllowPairingByName should be true")
	}
	if len(rec.gotParts) != 2 {
		t.Fatalf("want 2 parts (model.tmdl + .platform), got %d", len(rec.gotParts))
	}
	byPath := map[string]fabric.DefinitionPart{}
	for _, p := range rec.gotParts {
		byPath[p.Path] = p
	}
	mp, ok := byPath["/models/Sales.SemanticModel/definition/model.tmdl"]
	if !ok {
		t.Fatalf("model part missing; paths = %v", rec.gotParts)
	}
	if dec, _ := base64.StdEncoding.DecodeString(mp.Payload); string(dec) != "model Sales" {
		t.Errorf("model payload decoded = %q", dec)
	}
	plat, ok := byPath["/models/Sales.SemanticModel/.platform"]
	if !ok {
		t.Fatal(".platform part missing")
	}
	platDec, _ := base64.StdEncoding.DecodeString(plat.Payload)
	if strings.Contains(string(platDec), "GIT-LID") {
		t.Errorf(".platform logicalId not stripped: %s", platDec)
	}
	if !strings.Contains(string(platDec), `"displayName":"Sales"`) {
		t.Errorf(".platform displayName lost: %s", platDec)
	}

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Action != ActionCreate || results[0].ID != "new-id" || results[0].Name != "Sales" {
		t.Errorf("result = %+v", results[0])
	}
}

func TestBulkResultsToResultsStatusMapping(t *testing.T) {
	details := []fabric.BulkImportDetail{
		{ItemDisplayName: "A", ItemType: "Report", ItemID: "a", OperationType: "Update", OperationStatus: "Succeeded"},
		{ItemDisplayName: "B", ItemType: "Report", ItemID: "b", OperationType: "Create", OperationStatus: "SucceededDespiteFailures"},
		{ItemDisplayName: "C", ItemType: "Report", ItemID: "c", OperationType: "Create", OperationStatus: "Failed"},
	}
	got := bulkResultsToResults(details)
	if got[0].Action != ActionUpdate || got[0].Err != nil || got[0].Warning != "" {
		t.Errorf("A = %+v", got[0])
	}
	if got[1].Warning == "" || got[1].Err != nil {
		t.Errorf("B should be a warning, got %+v", got[1])
	}
	if got[2].Err == nil {
		t.Errorf("C should be an error, got %+v", got[2])
	}
}

func TestStripLogicalID(t *testing.T) {
	in := []byte(`{"$schema":"s","metadata":{"type":"Report","displayName":"R","description":"d"},"config":{"version":"2.0","logicalId":"GIT-LID"}}`)
	out := stripLogicalID(in)
	if strings.Contains(string(out), "GIT-LID") {
		t.Errorf("logicalId not stripped: %s", out)
	}
	if !strings.Contains(string(out), `"displayName":"R"`) {
		t.Errorf("displayName lost: %s", out)
	}
	if !strings.Contains(string(out), `"description":"d"`) {
		t.Errorf("description lost: %s", out)
	}
	if !strings.Contains(string(out), `"version":"2.0"`) {
		t.Errorf("config.version lost: %s", out)
	}
}

func TestStripLogicalIDNoConfigIsNoop(t *testing.T) {
	in := []byte(`{"metadata":{"type":"Report","displayName":"R"}}`)
	out := stripLogicalID(in)
	if string(out) != string(in) {
		t.Errorf("expected unchanged, got %s", out)
	}
}

func TestStripLogicalIDInvalidJSONIsNoop(t *testing.T) {
	in := []byte(`not json`)
	if string(stripLogicalID(in)) != string(in) {
		t.Error("invalid JSON should be returned unchanged")
	}
}
