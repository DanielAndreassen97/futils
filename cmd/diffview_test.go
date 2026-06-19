package cmd

import "testing"

func TestUnifiedLineDiffChangedLine(t *testing.T) {
	old := "a\nGUID-DEV\nc"
	new := "a\nGUID-TEST\nc"
	lines := unifiedLineDiff(old, new)
	// Expect: " a", "-GUID-DEV", "+GUID-TEST", " c"
	var minus, plus, ctx int
	for _, l := range lines {
		switch l.Op {
		case '-':
			minus++
			if l.Text != "GUID-DEV" {
				t.Errorf("unexpected removed line %q", l.Text)
			}
		case '+':
			plus++
			if l.Text != "GUID-TEST" {
				t.Errorf("unexpected added line %q", l.Text)
			}
		case ' ':
			ctx++
		}
	}
	if minus != 1 || plus != 1 || ctx != 2 {
		t.Fatalf("diff counts: -%d +%d ctx%d, want -1 +1 ctx2 (%#v)", minus, plus, ctx, lines)
	}
}

func TestUnifiedLineDiffIdentical(t *testing.T) {
	lines := unifiedLineDiff("x\ny", "x\ny")
	for _, l := range lines {
		if l.Op != ' ' {
			t.Errorf("identical input produced a non-context line: %#v", l)
		}
	}
}

func TestUnifiedLineDiffAddedAndRemoved(t *testing.T) {
	lines := unifiedLineDiff("keep\nold", "keep\nnew\nextra")
	var got string
	for _, l := range lines {
		got += string(l.Op) + l.Text + "\n"
	}
	// "keep" is common; "old" removed; "new" and "extra" added (order: removals before additions at a divergence is acceptable).
	if !contains(got, " keep\n") || !contains(got, "-old\n") || !contains(got, "+new\n") || !contains(got, "+extra\n") {
		t.Fatalf("diff missing expected lines:\n%s", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (stringIndex(s, sub) >= 0) }
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
