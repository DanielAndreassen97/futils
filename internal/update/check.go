// Package update checks GitHub Releases for a newer futils version — best
// effort, cached for a day, and never in the way of startup: the caller gives
// a small deadline, a fresh cache answers instantly, and a live check that
// loses the race still lands in the cache for the next launch.
package update

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// releaseURL is GitHub's latest-release endpoint for futils. Unauthenticated
// (60 req/h per IP) — the daily cache keeps usage at ~1 request per day.
// Var so tests can point it at a local server.
var releaseURL = "https://api.github.com/repos/DanielAndreassen97/futils/releases/latest"

// cacheTTL is how long a check result is trusted before asking GitHub again.
const cacheTTL = 24 * time.Hour

// fetchTimeout bounds the live HTTP check itself — generous, because a fetch
// that loses the caller's deadline still completes in the background and
// caches its result.
const fetchTimeout = 3 * time.Second

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// Notice returns a one-line upgrade hint ("v0.9.0 available — …") when a
// release newer than current exists, or "" — also on dev builds, unparseable
// versions, network failure, or when the deadline passes first. Safe to call
// on every interactive startup.
func Notice(current string, deadline time.Duration) string {
	if _, ok := parseSemver(current); !ok {
		return "" // dev build or unrecognizable version — nothing to compare
	}

	if latest, fresh := readCache(); fresh {
		return noticeText(current, latest)
	}

	result := make(chan string, 1)
	go func() {
		latest := fetchLatest()
		if latest != "" {
			writeCache(latest)
		}
		result <- latest
	}()
	select {
	case latest := <-result:
		return noticeText(current, latest)
	case <-time.After(deadline):
		return "" // too slow for this launch; the goroutine caches for the next
	}
}

// noticeText formats the upgrade hint, or "" when latest isn't newer.
func noticeText(current, latest string) string {
	if latest == "" || !newerVersion(current, latest) {
		return ""
	}
	return "v" + strings.TrimPrefix(latest, "v") +
		" available — brew upgrade futils · scoop update futils"
}

// newerVersion reports whether latest is a strictly newer semver than
// current. Unparseable input is never "newer".
func newerVersion(current, latest string) bool {
	c, okC := parseSemver(current)
	l, okL := parseSemver(latest)
	if !okC || !okL {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// parseSemver parses "1.2.3" / "v1.2.3" into its numeric triplet.
func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// fetchLatest asks GitHub for the newest release tag; "" on any failure.
func fetchLatest() string {
	req, err := http.NewRequest("GET", releaseURL, nil)
	if err != nil {
		return ""
	}
	// GitHub's API requires a User-Agent.
	req.Header.Set("User-Agent", "futils-update-check")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}
	return body.TagName
}

// cachePath is the check's state file; empty when no cache dir exists.
// Overridable in tests.
var cachePath = func() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "futils", "update-check.json")
}

func readCache() (latest string, fresh bool) {
	p := cachePath()
	if p == "" {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var c cacheFile
	if json.Unmarshal(raw, &c) != nil {
		return "", false
	}
	if time.Since(c.CheckedAt) > cacheTTL {
		return "", false
	}
	return c.Latest, true
}

func writeCache(latest string) {
	p := cachePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(cacheFile{CheckedAt: time.Now(), Latest: latest})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, raw, 0o644)
}
