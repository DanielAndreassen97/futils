package fabric

import "testing"

func TestParseTMDLPartition(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantTable   string
		wantKind    string
		wantRefresh bool // shorthand: kind == partitionTypeM and table != ""
	}{
		{
			name: "simple M partition",
			content: "table 'Sales'\n" +
				"\tpartition Sales = m\n" +
				"\t\tsource = ...",
			wantTable: "Sales", wantKind: "m", wantRefresh: true,
		},
		{
			name: "trailing annotation after kind does not break classification",
			// Regression: previous parser took everything after `=` as the
			// kind, so "m AnnotationName=Value" was classified as
			// "m annotationname=value" and the table was silently dropped.
			content: "table 'Foo'\n" +
				"\tpartition Foo = m AnnotationName=ValueGoesHere\n",
			wantTable: "Foo", wantKind: "m", wantRefresh: true,
		},
		{
			name: "trailing whitespace tokens after kind",
			content: "table 'Bar'\n" +
				"\tpartition Bar = m   \t  // sometrailing\n",
			wantTable: "Bar", wantKind: "m", wantRefresh: true,
		},
		{
			name: "hybrid table — calculated comes first, then M",
			// Regression: previous parser returned on the first partition
			// line, so a calculated partition listed before an M partition
			// downgraded the whole table to non-refreshable.
			content: "table 'Hybrid'\n" +
				"\tpartition Calc = calculated\n" +
				"\t\texpression = ...\n" +
				"\tpartition Live = m\n" +
				"\t\tsource = ...",
			wantTable: "Hybrid", wantKind: "m", wantRefresh: true,
		},
		{
			name: "M partition first then calculated — still refreshable",
			content: "table 'Hybrid2'\n" +
				"\tpartition Live = m\n" +
				"\tpartition Calc = calculated\n",
			wantTable: "Hybrid2", wantKind: "m", wantRefresh: true,
		},
		{
			name: "calculated-only table is not refreshable",
			content: "table 'CalcOnly'\n" +
				"\tpartition Calc = calculated\n",
			wantTable: "CalcOnly", wantKind: "calculated", wantRefresh: false,
		},
		{
			name:      "no table or partition lines",
			content:   "/// some leading comment\nannotation Foo = Bar\n",
			wantTable: "", wantKind: "", wantRefresh: false,
		},
		{
			name: "case-insensitive M token",
			content: "table 'Cased'\n" +
				"\tpartition Cased = M\n",
			wantTable: "Cased", wantKind: "m", wantRefresh: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTable, gotKind := parseTMDLPartition(tc.content)
			if gotTable != tc.wantTable {
				t.Errorf("tableName: got %q, want %q", gotTable, tc.wantTable)
			}
			if gotKind != tc.wantKind {
				t.Errorf("partitionKind: got %q, want %q", gotKind, tc.wantKind)
			}
			refreshable := gotTable != "" && gotKind == partitionTypeM
			if refreshable != tc.wantRefresh {
				t.Errorf("refreshable: got %v, want %v", refreshable, tc.wantRefresh)
			}
		})
	}
}
