package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// writeHistoryReport writes htmlDoc into dir as a timestamped file
// (YYYYMMDD_HHMM.html), creating dir if needed. ts is passed in (not read from
// the clock) so the filename is deterministic and testable. On a same-minute
// collision it appends -2, -3, … rather than overwriting. Returns the path written.
func writeHistoryReport(dir string, ts time.Time, htmlDoc string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create history dir: %w", err)
	}
	base := ts.Format("20060102_1504")
	name := base + ".html"
	for n := 2; ; n++ {
		full := filepath.Join(dir, name)
		_, statErr := os.Stat(full)
		if os.IsNotExist(statErr) {
			if err := os.WriteFile(full, []byte(htmlDoc), 0o644); err != nil {
				return "", fmt.Errorf("write history report: %w", err)
			}
			return full, nil
		}
		if statErr != nil {
			return "", statErr
		}
		name = fmt.Sprintf("%s-%d.html", base, n)
	}
}
