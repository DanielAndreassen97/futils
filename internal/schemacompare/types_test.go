package schemacompare

import "testing"

func TestFormatType(t *testing.T) {
	cases := []struct {
		name      string
		typeName  string
		precision int
		scale     int
		want      string
	}{
		{"string", "string", 0, 0, "string"},
		{"long", "long", 0, 0, "long"},
		{"decimal", "decimal", 18, 2, "decimal(18,2)"},
		{"decimal zero precision falls back to name", "decimal", 0, 0, "decimal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatType(c.typeName, c.precision, c.scale); got != c.want {
				t.Errorf("formatType(%q,%d,%d) = %q, want %q", c.typeName, c.precision, c.scale, got, c.want)
			}
		})
	}
}

func TestTableKey(t *testing.T) {
	if got := TableKey("Dim", "Ansatt"); got != "Dim.Ansatt" {
		t.Errorf("TableKey = %q, want Dim.Ansatt", got)
	}
}
