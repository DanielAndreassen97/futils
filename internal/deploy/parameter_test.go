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

func TestApplyFindReplaceLiteralAndRegex(t *testing.T) {
	p := Parameters{FindReplace: []FindReplace{
		{ // literal, Notebook-only
			FindValue:    "DEV-GUID",
			ReplaceValue: map[string]string{"TEST": "TEST-GUID"},
			ItemType:     StringOrSlice{"Notebook"},
		},
		{ // regex, all envs via _ALL_
			FindValue:    `lakehouse://([0-9]+)`,
			ReplaceValue: map[string]string{"_ALL_": "lakehouse://X"},
			IsRegex:      "true",
		},
	}}
	identity := func(s string) (string, error) { return s, nil }

	nb := LocalItem{Type: "Notebook", DisplayName: "NB_A"}
	in := []byte("id=DEV-GUID path=lakehouse://123")
	out, err := p.ApplyFindReplace("TEST", nb, "notebook-content.py", in, identity)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "id=TEST-GUID path=lakehouse://X" {
		t.Errorf("got %q", out)
	}
}

func TestApplyFindReplaceSkipsOnTypeMismatch(t *testing.T) {
	p := Parameters{FindReplace: []FindReplace{{
		FindValue:    "DEV-GUID",
		ReplaceValue: map[string]string{"TEST": "TEST-GUID"},
		ItemType:     StringOrSlice{"Notebook"},
	}}}
	identity := func(s string) (string, error) { return s, nil }
	report := LocalItem{Type: "Report", DisplayName: "R"}
	out, _ := p.ApplyFindReplace("TEST", report, "report.json", []byte("DEV-GUID"), identity)
	if string(out) != "DEV-GUID" {
		t.Errorf("type filter should have skipped; got %q", out)
	}
}

func TestApplyFindReplaceIgnoreCase(t *testing.T) {
	p := Parameters{FindReplace: []FindReplace{{
		FindValue:    "dev-guid",
		ReplaceValue: map[string]string{"_ALL_": "X"},
		IsRegex:      "true",
		IgnoreCase:   "true",
	}}}
	identity := func(s string) (string, error) { return s, nil }
	out, err := p.ApplyFindReplace("TEST", LocalItem{Type: "Notebook"}, "f.py", []byte("id=DEV-GUID"), identity)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "id=X" {
		t.Errorf("ignore_case regex should match DEV-GUID; got %q", out)
	}
}

func TestApplyFindReplaceResolvesDynamicValue(t *testing.T) {
	p := Parameters{FindReplace: []FindReplace{{
		FindValue:    "DEV-GUID",
		ReplaceValue: map[string]string{"_ALL_": "$items.Lakehouse.LH_Config.$id"},
	}}}
	resolve := func(s string) (string, error) {
		if s == "$items.Lakehouse.LH_Config.$id" {
			return "resolved-guid", nil
		}
		return s, nil
	}
	out, err := p.ApplyFindReplace("TEST", LocalItem{Type: "Notebook"}, "f.py", []byte("DEV-GUID"), resolve)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if string(out) != "resolved-guid" {
		t.Errorf("got %q", out)
	}
}
