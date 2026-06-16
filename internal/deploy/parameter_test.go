package deploy

import (
	"reflect"
	"testing"
)

func TestParseParametersStringOrSlice(t *testing.T) {
	raw := []byte(`
find_replace:
  - find_value: "DEV-GUID"
    replace_value:
      TEST: "TEST-GUID"
      PROD: "PROD-GUID"
    item_type: "Notebook"
    item_name:
      - "NB_A"
      - "NB_B"
`)
	p, err := ParseParameters(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.FindReplace) != 1 {
		t.Fatalf("got %d find_replace", len(p.FindReplace))
	}
	fr := p.FindReplace[0]
	if fr.FindValue != "DEV-GUID" {
		t.Errorf("find_value = %q", fr.FindValue)
	}
	if !reflect.DeepEqual([]string(fr.ItemType), []string{"Notebook"}) {
		t.Errorf("item_type = %v", fr.ItemType)
	}
	if !reflect.DeepEqual([]string(fr.ItemName), []string{"NB_A", "NB_B"}) {
		t.Errorf("item_name = %v", fr.ItemName)
	}
	if fr.ReplaceValue["TEST"] != "TEST-GUID" {
		t.Errorf("replace TEST = %q", fr.ReplaceValue["TEST"])
	}
}

func TestParseParametersEmpty(t *testing.T) {
	p, err := ParseParameters([]byte(""))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(p.FindReplace) != 0 {
		t.Errorf("expected no rules, got %d", len(p.FindReplace))
	}
}
