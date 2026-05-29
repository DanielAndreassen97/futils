package ui

import "testing"

func TestFitWidth(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"short string is padded with trailing spaces", "abc", 5, "abc  "},
		{"exact width is unchanged", "abcde", 5, "abcde"},
		{"long string is truncated with an ellipsis", "abcdef", 5, "abcd…"},
		{"counts runes not bytes when padding", "ærø", 5, "ærø  "},
		{"counts runes not bytes when truncating", "æøåxy", 4, "æøå…"},
		{"width of one yields just the ellipsis", "abcdef", 1, "…"},
		{"non-positive width yields empty string", "abc", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FitWidth(tc.in, tc.width); got != tc.want {
				t.Errorf("FitWidth(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
		})
	}
}
