package fabric

import (
	"reflect"
	"testing"
)

func TestParseParameters_FabricSourceAsArray(t *testing.T) {
	// Fabric's getDefinition returns .ipynb with source as []string — each
	// element is a line including its trailing newline.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": [
					"# Default values — override at job submission time\n",
					"specific_table_names = \"Fremmote\"\n",
					"rewrite_table = False\n",
					"batch_size = 1000\n",
					"sample_rate = 0.25\n"
				]
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	want := []Parameter{
		{Name: "specific_table_names", Type: "string", Default: "Fremmote", RawDefault: `"Fremmote"`},
		{Name: "rewrite_table", Type: "bool", Default: false, RawDefault: "False"},
		{Name: "batch_size", Type: "int", Default: int64(1000), RawDefault: "1000"},
		{Name: "sample_rate", Type: "float", Default: 0.25, RawDefault: "0.25"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseParameters_SourceAsString(t *testing.T) {
	// Some exports (and hand-edited notebooks) use a single string for source.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "name = 'alice'\nage = 30\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	want := []Parameter{
		{Name: "name", Type: "string", Default: "alice", RawDefault: `'alice'`},
		{Name: "age", Type: "int", Default: int64(30), RawDefault: "30"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseParameters_SkipsNonParameterCells(t *testing.T) {
	// Cells without the "parameters" tag must be ignored, even if they
	// contain assignments that look like parameters.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["setup"]},
				"source": "helper_value = \"IGNORE ME\"\n"
			},
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "real_param = \"keep\"\n"
			},
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "second_params_cell = \"also ignored\"\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	if len(got) != 1 || got[0].Name != "real_param" {
		t.Fatalf("expected only real_param from first tagged cell, got: %#v", got)
	}
}

func TestParseParameters_InlineCommentsStripped(t *testing.T) {
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "table = \"Fakta\" # table to refresh\nrows = 100  # row count\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	want := []Parameter{
		{Name: "table", Type: "string", Default: "Fakta", RawDefault: `"Fakta"`},
		{Name: "rows", Type: "int", Default: int64(100), RawDefault: "100"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseParameters_HashInsideStringKept(t *testing.T) {
	// A `#` inside a string must NOT be treated as a comment.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "color = \"#ff00aa\"\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	if len(got) != 1 || got[0].Default != "#ff00aa" {
		t.Fatalf("expected color=#ff00aa, got: %#v", got)
	}
}

func TestParseParameters_TypeAnnotationStripped(t *testing.T) {
	// Some authors write `x: int = 5` — we should still accept it.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "batch_size: int = 500\nname: str = \"prod\"\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	want := []Parameter{
		{Name: "batch_size", Type: "int", Default: int64(500), RawDefault: "500"},
		{Name: "name", Type: "string", Default: "prod", RawDefault: `"prod"`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseParameters_SkipsUnsupportedLiterals(t *testing.T) {
	// Lists, dicts, None, expressions, f-strings — all skipped because
	// Fabric's RunNotebook API doesn't accept those types anyway.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": [
					"keep_this = \"yes\"\n",
					"skip_list = [1, 2, 3]\n",
					"skip_dict = {\"a\": 1}\n",
					"skip_none = None\n",
					"skip_expr = 1 + 2\n",
					"skip_fstring = f\"hi {name}\"\n",
					"keep_also = 42\n"
				]
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	names := make([]string, len(got))
	for i, p := range got {
		names[i] = p.Name
	}
	want := []string{"keep_this", "keep_also"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("expected only %v kept, got %v (full: %#v)", want, names, got)
	}
}

func TestParseParameters_NoParametersCell(t *testing.T) {
	// A notebook with no parameters cell returns an empty slice and no error.
	nb := []byte(`{
		"cells": [
			{"cell_type": "code", "metadata": {}, "source": "print('hi')\n"},
			{"cell_type": "markdown", "metadata": {}, "source": "# Title\n"}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got: %#v", got)
	}
}

func TestParseParameters_EmptyParametersCell(t *testing.T) {
	// Cell tagged but with only comments / whitespace.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "# Parameters go here\n\n# (none yet)\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got: %#v", got)
	}
}

func TestParseParameters_NegativeNumbers(t *testing.T) {
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "offset = -10\nratio = -0.5\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}

	want := []Parameter{
		{Name: "offset", Type: "int", Default: int64(-10), RawDefault: "-10"},
		{Name: "ratio", Type: "float", Default: -0.5, RawDefault: "-0.5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parameters mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseParameters_SingleQuotedStringWithEscapes(t *testing.T) {
	// Python allows `'it\'s'` — our parser should unquote it correctly.
	nb := []byte(`{
		"cells": [
			{
				"cell_type": "code",
				"metadata": {"tags": ["parameters"]},
				"source": "greeting = 'it\\'s Monday'\n"
			}
		]
	}`)

	got, err := ParseParameters(nb)
	if err != nil {
		t.Fatalf("ParseParameters: %v", err)
	}
	if len(got) != 1 || got[0].Default != "it's Monday" {
		t.Fatalf("expected escaped single-quote string, got: %#v", got)
	}
}

func TestParseParameters_MalformedJSON(t *testing.T) {
	_, err := ParseParameters([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
