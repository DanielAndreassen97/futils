package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestNewerTag(t *testing.T) {
	if got := newerTag("0.8.0", "v0.9.0"); got != "v0.9.0" {
		t.Errorf("newerTag = %q, want v0.9.0", got)
	}
	// A bare tag from the API is normalised to the v-prefixed form the banner draws.
	if got := newerTag("0.8.0", "0.9.0"); got != "v0.9.0" {
		t.Errorf("newerTag must normalise a bare tag, got %q", got)
	}
	if got := newerTag("0.9.0", "v0.9.0"); got != "" {
		t.Errorf("up-to-date must yield no tag, got %q", got)
	}
	if got := newerTag("0.9.0", ""); got != "" {
		t.Errorf("failed fetch must yield no tag, got %q", got)
	}
}

// A dev build never checks anything — Notice returns "" without touching the
// network or cache.
func TestAvailableSkipsDevBuilds(t *testing.T) {
	if got := Available("dev", time.Second); got != "" {
		t.Errorf("dev build must never produce a notice, got %q", got)
	}
}

// A fresh cache answers without any network; a stale one is ignored.
func TestAvailableUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	orig := cachePath
	cachePath = func() string { return filepath.Join(dir, "update-check.json") }
	t.Cleanup(func() { cachePath = orig })

	raw, _ := json.Marshal(cacheFile{CheckedAt: time.Now(), Latest: "v9.9.9"})
	if err := os.WriteFile(cachePath(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Available("0.8.0", time.Second); got != "v9.9.9" {
		t.Errorf("fresh cache must answer the check, got %q", got)
	}

	// The TTL is what decides how fast a release reaches users, so pin both
	// sides of it: an hour old still answers, seven hours old does not.
	hourOld, _ := json.Marshal(cacheFile{CheckedAt: time.Now().Add(-1 * time.Hour), Latest: "v9.9.9"})
	if err := os.WriteFile(cachePath(), hourOld, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, fresh := readCache(); !fresh {
		t.Error("a 1h cache must still count as fresh")
	}

	stale, _ := json.Marshal(cacheFile{CheckedAt: time.Now().Add(-7 * time.Hour), Latest: "v9.9.9"})
	if err := os.WriteFile(cachePath(), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, fresh := readCache(); fresh {
		t.Error("a 7h cache must not count as fresh — the TTL is 6h")
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
func TestAvailableLiveFetchAndCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.9.0"}`))
	}))
	defer srv.Close()
	origURL, origPath := releaseURL, cachePath
	releaseURL = srv.URL
	dir := t.TempDir()
	cachePath = func() string { return filepath.Join(dir, "update-check.json") }
	t.Cleanup(func() { releaseURL, cachePath = origURL, origPath })

	if got := Available("0.8.0", 2*time.Second); got != "v0.9.0" {
		t.Fatalf("live check must produce the tag, got %q", got)
	}
	if latest, fresh := readCache(); !fresh || latest != "v0.9.0" {
		t.Errorf("live check must cache its result, got %q fresh=%v", latest, fresh)
	}
}
