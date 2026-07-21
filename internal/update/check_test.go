package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.8.0", "v0.9.0", true},
		{"0.8.0", "v0.8.1", true},
		{"0.8.0", "v1.0.0", true},
		{"0.8.0", "v0.8.0", false},
		{"0.9.0", "v0.8.9", false},
		{"1.0.0", "v0.99.99", false},
		{"dev", "v9.9.9", false},
		{"0.8.0", "not-a-version", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.current, c.latest); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestNoticeText(t *testing.T) {
	if got := noticeText("0.8.0", "v0.9.0"); !strings.Contains(got, "v0.9.0 available") || !strings.Contains(got, "brew upgrade futils") {
		t.Errorf("notice must name the version and the upgrade command, got %q", got)
	}
	if got := noticeText("0.9.0", "v0.9.0"); got != "" {
		t.Errorf("up-to-date must yield no notice, got %q", got)
	}
	if got := noticeText("0.9.0", ""); got != "" {
		t.Errorf("failed fetch must yield no notice, got %q", got)
	}
}

// A dev build never checks anything — Notice returns "" without touching the
// network or cache.
func TestNoticeSkipsDevBuilds(t *testing.T) {
	if got := Notice("dev", time.Second); got != "" {
		t.Errorf("dev build must never produce a notice, got %q", got)
	}
}

// A fresh cache answers without any network; a stale one is ignored.
func TestNoticeUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	orig := cachePath
	cachePath = func() string { return filepath.Join(dir, "update-check.json") }
	t.Cleanup(func() { cachePath = orig })

	raw, _ := json.Marshal(cacheFile{CheckedAt: time.Now(), Latest: "v9.9.9"})
	if err := os.WriteFile(cachePath(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Notice("0.8.0", time.Second); !strings.Contains(got, "v9.9.9") {
		t.Errorf("fresh cache must answer the check, got %q", got)
	}

	stale, _ := json.Marshal(cacheFile{CheckedAt: time.Now().Add(-25 * time.Hour), Latest: "v9.9.9"})
	if err := os.WriteFile(cachePath(), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, fresh := readCache(); fresh {
		t.Error("a >24h cache must not count as fresh")
	}
}

func TestFetchLatestParsesTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("GitHub requires a User-Agent header")
		}
		w.Write([]byte(`{"tag_name":"v0.9.0"}`))
	}))
	defer srv.Close()
	orig := releaseURL
	releaseURL = srv.URL
	t.Cleanup(func() { releaseURL = orig })

	if got := fetchLatest(); got != "v0.9.0" {
		t.Fatalf("fetchLatest = %q, want v0.9.0", got)
	}
}

// Notice end-to-end against a fake GitHub: live fetch inside the deadline
// produces the hint and writes the cache.
func TestNoticeLiveFetchAndCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.9.0"}`))
	}))
	defer srv.Close()
	origURL, origPath := releaseURL, cachePath
	releaseURL = srv.URL
	dir := t.TempDir()
	cachePath = func() string { return filepath.Join(dir, "update-check.json") }
	t.Cleanup(func() { releaseURL, cachePath = origURL, origPath })

	if got := Notice("0.8.0", 2*time.Second); !strings.Contains(got, "v0.9.0 available") {
		t.Fatalf("live check must produce the hint, got %q", got)
	}
	if latest, fresh := readCache(); !fresh || latest != "v0.9.0" {
		t.Errorf("live check must cache its result, got %q fresh=%v", latest, fresh)
	}
}
