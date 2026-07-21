package ui

import (
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

func TestCollectOverrides_OnlyChangedValuesEmitted(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "specific_table_names", Type: fabric.TypeString, Default: "", RawDefault: "''"},
		{Name: "rewrite_table", Type: fabric.TypeBool, Default: false, RawDefault: "False"},
		{Name: "batch_name", Type: fabric.TypeString, Default: "Dim", RawDefault: "'Dim'"},
	}

	// User typed an override for specific_table_names, toggled rewrite_table
	// to True, and LEFT batch_name untouched. Only the first two should be
	// emitted as JobInputs.
	text := []string{"TidType,Tariffavtale", "", ""}
	bool_ := []bool{false, true, false}

	got, err := collectOverrides(params, text, bool_, false)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %d: %#v", len(got), got)
	}
	if got[0].Name != "specific_table_names" || got[0].Value != "TidType,Tariffavtale" {
		t.Errorf("specific_table_names override wrong: %#v", got[0])
	}
	if got[1].Name != "rewrite_table" || got[1].Value != true {
		t.Errorf("rewrite_table override wrong: %#v", got[1])
	}
}

func TestCollectOverrides_EmptyStringSkippedEvenWhenDefaultIsEmpty(t *testing.T) {
	// This is the Fabric-400 gotcha: sending {"value":""} trips the server.
	// An empty user value means "keep notebook default", which is also empty
	// in this case — so we emit nothing.
	params := []fabric.Parameter{
		{Name: "tag", Type: fabric.TypeString, Default: "", RawDefault: "''"},
	}
	text := []string{""}
	bool_ := []bool{false}

	got, err := collectOverrides(params, text, bool_, false)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero overrides, got %#v", got)
	}
}

func TestCollectOverrides_SameValueAsDefaultSkipped(t *testing.T) {
	// User re-typed the exact default — we should still skip to keep payloads
	// minimal and avoid redundant work on the server.
	params := []fabric.Parameter{
		{Name: "batch_name", Type: fabric.TypeString, Default: "Dim", RawDefault: "'Dim'"},
		{Name: "threads", Type: fabric.TypeInt, Default: int64(4), RawDefault: "4"},
		{Name: "rate", Type: fabric.TypeFloat, Default: 0.5, RawDefault: "0.5"},
	}
	text := []string{"Dim", "4", "0.5"}
	bool_ := []bool{false, false, false}

	got, err := collectOverrides(params, text, bool_, false)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero overrides, got %#v", got)
	}
}

func TestCollectOverrides_TypedCoercion(t *testing.T) {
	// The form stores numeric input as strings; collectOverrides must parse
	// them to the right Go type (int64 / float64) before handing to the
	// API layer so JSON encoding produces the right wire shape.
	params := []fabric.Parameter{
		{Name: "threads", Type: fabric.TypeInt, Default: int64(4), RawDefault: "4"},
		{Name: "rate", Type: fabric.TypeFloat, Default: 1.0, RawDefault: "1.0"},
	}
	text := []string{"16", "0.25"}
	bool_ := []bool{false, false}

	got, err := collectOverrides(params, text, bool_, false)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %#v", got)
	}
	if v, ok := got[0].Value.(int64); !ok || v != 16 {
		t.Errorf("expected int64(16), got %T(%v)", got[0].Value, got[0].Value)
	}
	if v, ok := got[1].Value.(float64); !ok || v != 0.25 {
		t.Errorf("expected float64(0.25), got %T(%v)", got[1].Value, got[1].Value)
	}
}

func TestCollectOverrides_BadNumericInputReturnsError(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "threads", Type: fabric.TypeInt, Default: int64(4), RawDefault: "4"},
	}
	text := []string{"not a number"}
	bool_ := []bool{false}

	if _, err := collectOverrides(params, text, bool_, false); err == nil {
		t.Fatal("expected error for non-numeric int input, got nil")
	}
}

// Prefill mode (pipelines): the box is seeded with the default, so an
// UNCHANGED box is omitted (server keeps the default) while a CLEARED box
// sends an explicit empty string — which placeholder mode can't express.
func TestCollectOverrides_PrefillEmptyStringIsExplicit(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "tags", Type: fabric.TypeString, Default: "a,b", RawDefault: "a,b"},   // left at default
		{Name: "suffix", Type: fabric.TypeString, Default: "_v1", RawDefault: "_v1"}, // cleared to ""
		{Name: "note", Type: fabric.TypeString, Default: "", RawDefault: ""},         // no default, left ""
	}
	// tags kept as-is, suffix cleared, note untouched.
	text := []string{"a,b", "", ""}
	bool_ := []bool{false, false, false}

	got, err := collectOverrides(params, text, bool_, true)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	// Only suffix changed (default "_v1" → ""). tags unchanged and note
	// (no default, empty) are both omitted.
	if len(got) != 1 {
		t.Fatalf("expected 1 override, got %d: %#v", len(got), got)
	}
	if got[0].Name != "suffix" || got[0].Value != "" {
		t.Errorf("expected explicit empty suffix, got %#v", got[0])
	}
}

// Placeholder mode (notebooks) cannot send an empty string: clearing a field
// reads as "keep default" and is omitted.
func TestCollectOverrides_PlaceholderEmptyKeepsDefault(t *testing.T) {
	params := []fabric.Parameter{
		{Name: "suffix", Type: fabric.TypeString, Default: "_v1", RawDefault: "'_v1'"},
	}
	got, err := collectOverrides(params, []string{""}, []bool{false}, false)
	if err != nil {
		t.Fatalf("collectOverrides: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("placeholder mode: empty must keep default (omit), got %#v", got)
	}
}
