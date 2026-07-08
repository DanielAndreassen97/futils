package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteHistoryReportCreatesTimestampedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "history") // non-existent → must be created
	ts := time.Date(2026, 6, 22, 13, 40, 0, 0, time.UTC)

	p1, err := writeHistoryReport(dir, ts, "<html>one</html>")
	if err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if filepath.Base(p1) != "20260622_1340.html" {
		t.Errorf("first filename = %q, want 20260622_1340.html", filepath.Base(p1))
	}
	got, err := os.ReadFile(p1)
	if err != nil || string(got) != "<html>one</html>" {
		t.Errorf("content = %q (err %v)", got, err)
	}

	// Same timestamp again → collision suffix, not an overwrite.
	p2, err := writeHistoryReport(dir, ts, "<html>two</html>")
	if err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if filepath.Base(p2) != "20260622_1340-2.html" {
		t.Errorf("collision filename = %q, want 20260622_1340-2.html", filepath.Base(p2))
	}
	if first, _ := os.ReadFile(p1); string(first) != "<html>one</html>" {
		t.Errorf("first file was overwritten: %q", first)
	}
}
