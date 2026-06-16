package deploy

import "testing"

func TestParsePlatform(t *testing.T) {
	raw := []byte(`{
	  "metadata": { "type": "Notebook", "displayName": "NB_Foo", "description": "does foo" },
	  "config": { "logicalId": "11111111-1111-1111-1111-111111111111" }
	}`)
	meta, err := parsePlatform(raw)
	if err != nil {
		t.Fatalf("parsePlatform: %v", err)
	}
	if meta.Type != "Notebook" || meta.DisplayName != "NB_Foo" {
		t.Errorf("got %+v", meta)
	}
	if meta.LogicalID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("logicalId = %q", meta.LogicalID)
	}
	if meta.Description != "does foo" {
		t.Errorf("description = %q", meta.Description)
	}
}

func TestParsePlatformRejectsMissingType(t *testing.T) {
	if _, err := parsePlatform([]byte(`{"metadata":{"displayName":"X"}}`)); err == nil {
		t.Fatal("expected error for missing type")
	}
}
