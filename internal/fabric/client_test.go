package fabric

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestThrottleDelayHonorsRetryAfter(t *testing.T) {
	if got := throttleDelay("10", 0); got != 10*time.Second {
		t.Errorf("got %v, want 10s", got)
	}
}

func TestNextPollIntervalBacksOff(t *testing.T) {
	// Floor matches the reference dry-run's steady 1s poll so we don't spike the
	// request rate past Fabric's limit; it then backs off to the 2s cap.
	cases := []struct{ prev, want time.Duration }{
		{0, 1 * time.Second},               // first wait after an immediate poll miss
		{1 * time.Second, 2 * time.Second}, // back off toward the cap
		{2 * time.Second, 2 * time.Second}, // capped
		{5 * time.Second, 2 * time.Second}, // never exceeds the cap
	}
	for _, c := range cases {
		if got := nextPollInterval(c.prev); got != c.want {
			t.Errorf("nextPollInterval(%v) = %v, want %v", c.prev, got, c.want)
		}
	}
}

func TestThrottleDelayCapsRetryAfter(t *testing.T) {
	// Retry-After=9999 is larger than the backoff at attempt 0 (10s),
	// so min(9999, 10) = 10s — backoff wins, no clamping needed.
	if got := throttleDelay("9999", 0); got != 10*time.Second {
		t.Errorf("got %v, want 10s", got)
	}
}

func TestThrottleDelayBackoffWithoutHeader(t *testing.T) {
	// No header → default raSecs=60. min(60, 10*2^2=40) = 40s.
	if got := throttleDelay("", 2); got != 40*time.Second {
		t.Errorf("got %v, want 40s", got)
	}
}

func TestParseLakehouseSqlEndpoint(t *testing.T) {
	body := []byte(`{
	  "id": "lh-1",
	  "properties": {
	    "sqlEndpointProperties": {
	      "connectionString": "abc.datawarehouse.fabric.microsoft.com",
	      "id": "ep-123"
	    }
	  }
	}`)
	host, id, err := parseLakehouseSqlEndpoint(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if host != "abc.datawarehouse.fabric.microsoft.com" || id != "ep-123" {
		t.Errorf("host=%q id=%q", host, id)
	}
}

func TestParseLakehouseSqlEndpointMissing(t *testing.T) {
	if _, _, err := parseLakehouseSqlEndpoint([]byte(`{"id":"lh","properties":{}}`)); err == nil {
		t.Fatal("expected error when sql endpoint absent")
	}
}

// seqTransport replays a fixed sequence of HTTP responses. Each call to
// RoundTrip returns the next response in the list; if the sequence is
// exhausted it returns the last entry repeatedly.
type seqTransport struct {
	responses []seqResponse
	calls     []string // Authorization header values recorded per call
}

type seqResponse struct {
	status     int
	retryAfter string
	body       string
}

func (s *seqTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, req.Header.Get("Authorization"))
	idx := len(s.calls) - 1
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	r := s.responses[idx]
	hdr := http.Header{}
	if r.retryAfter != "" {
		hdr.Set("Retry-After", r.retryAfter)
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     hdr,
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}, nil
}

// TestDoGetRefreshedTokenUsedAfterThrottle verifies that when doGet encounters
// a 401 (token expired), refreshes to a new token, then hits a 429 (throttle),
// the subsequent retry uses the REFRESHED token — not the original stale one.
//
// Without the fix: the loop continues with the original `token` parameter, so
// attempt N+1 sends "Bearer stale-token" instead of "Bearer fresh-token".
func TestDoGetRefreshedTokenUsedAfterThrottle(t *testing.T) {
	transport := &seqTransport{
		responses: []seqResponse{
			{status: http.StatusUnauthorized, body: "expired"},    // call 1: stale token
			{status: http.StatusTooManyRequests, retryAfter: "0"}, // call 2: after refresh (429)
			{status: http.StatusOK, body: `"ok"`},                 // call 3: after throttle wait
		},
	}

	// Swap the package-level HTTP client and token-refresh hook.
	origClient := httpClient
	origRetry := retryTokenFn
	t.Cleanup(func() {
		httpClient = origClient
		retryTokenFn = origRetry
	})
	httpClient = &http.Client{Transport: transport}
	retryTokenFn = func() (string, bool) { return "fresh-token", true }

	body, err := doGet("stale-token", "http://example.invalid/api")
	if err != nil {
		t.Fatalf("doGet returned error: %v", err)
	}
	if string(body) != `"ok"` {
		t.Fatalf("unexpected body: %q", string(body))
	}

	// There must be exactly 3 calls.
	if len(transport.calls) != 3 {
		t.Fatalf("expected 3 HTTP calls, got %d: %v", len(transport.calls), transport.calls)
	}
	// Call 1: stale token.
	if transport.calls[0] != "Bearer stale-token" {
		t.Errorf("call 1 auth = %q, want Bearer stale-token", transport.calls[0])
	}
	// Call 2: the refresh was triggered by the 401; must carry the fresh token.
	if transport.calls[1] != "Bearer fresh-token" {
		t.Errorf("call 2 auth = %q, want Bearer fresh-token", transport.calls[1])
	}
	// Call 3: after throttle wait; must STILL carry the fresh token (the bug).
	if transport.calls[2] != "Bearer fresh-token" {
		t.Errorf("call 3 auth = %q, want Bearer fresh-token (was stale token reused after throttle?)", transport.calls[2])
	}
}

func TestThrottleDelayFabricCICD(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter string
		attempt    int
		want       time.Duration
	}{
		// No header — defaults to 60s, min(60, 10*2^attempt)
		{"no-header-attempt-0", "", 0, 10 * time.Second},
		{"no-header-attempt-1", "", 1, 20 * time.Second},
		{"no-header-attempt-2", "", 2, 40 * time.Second},
		{"no-header-attempt-3", "", 3, 60 * time.Second}, // min(60,80)=80→clamp 60
		{"no-header-attempt-4", "", 4, 60 * time.Second}, // min(60,160)=160→clamp 60
		// Attempts 5, 6, 7 — no header → 60s (10·2^a saturated, clamp to 60)
		{"no-header-attempt-5", "", 5, 60 * time.Second},
		{"no-header-attempt-6", "", 6, 60 * time.Second},
		{"no-header-attempt-7", "", 7, 60 * time.Second},
		// Retry-After=5: always wins
		{"retry-after-5-attempt-0", "5", 0, 5 * time.Second}, // min(5,10)=5
		{"retry-after-5-attempt-2", "5", 2, 5 * time.Second}, // min(5,40)=5
		// Retry-After=120: backoff wins early, clamp later
		{"retry-after-120-attempt-0", "120", 0, 10 * time.Second}, // min(120,10)=10
		{"retry-after-120-attempt-3", "120", 3, 60 * time.Second}, // min(120,80)=80→clamp 60
		// Invalid/zero → default 60s path
		{"invalid-header", "abc", 0, 10 * time.Second},
		{"zero-header", "0", 0, 10 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := throttleDelay(c.retryAfter, c.attempt)
			if got != c.want {
				t.Errorf("throttleDelay(%q, %d) = %v, want %v", c.retryAfter, c.attempt, got, c.want)
			}
		})
	}
}

func TestThrottleStateExposed(t *testing.T) {
	t.Cleanup(clearThrottleState)
	clearThrottleState()

	noteThrottle(8*time.Second, 1)
	if got := ThrottleAttempt(); got != 2 {
		t.Errorf("ThrottleAttempt() = %d, want 2", got)
	}
	if got := ThrottleTotal(); got != 8*time.Second {
		t.Errorf("ThrottleTotal() = %v, want 8s", got)
	}
	rem := ThrottleRemaining()
	if rem <= 6*time.Second || rem > 8*time.Second {
		t.Errorf("ThrottleRemaining() = %v, want in (6s, 8s]", rem)
	}

	// Call with shorter backoff — deadline must NOT move earlier
	noteThrottle(2*time.Second, 0)
	rem2 := ThrottleRemaining()
	// The 8s deadline should still be in effect (well past 2s from now)
	if rem2 < 5*time.Second {
		t.Errorf("ThrottleRemaining() after short noteThrottle = %v, want still > 5s (longest-wins should preserve longer deadline)", rem2)
	}
}

// TestThrottleSnapshot verifies the torn-free view: a single lock acquisition
// ensures remaining never exceeds total, the longest-deadline-wins rule, and
// clearThrottleState zeroes all fields correctly.
func TestThrottleSnapshot(t *testing.T) {
	t.Cleanup(clearThrottleState)
	clearThrottleState()

	// After an 8s backoff at attempt 1: attempt field should be 2, total 8s, remaining ∈ (6s,8s].
	noteThrottle(8*time.Second, 1)
	_, rem, tot, attempt := ThrottleSnapshot()
	if attempt != 2 {
		t.Errorf("attempt = %d, want 2", attempt)
	}
	if tot != 8*time.Second {
		t.Errorf("total = %v, want 8s", tot)
	}
	if rem <= 6*time.Second || rem > 8*time.Second {
		t.Errorf("remaining = %v, want in (6s, 8s]", rem)
	}
	// Core invariant: remaining must never exceed total (no torn reads).
	if rem > tot {
		t.Errorf("remaining %v > total %v — torn read", rem, tot)
	}

	// Shorter backoff must NOT replace the longer deadline.
	noteThrottle(2*time.Second, 0)
	_, rem2, tot2, _ := ThrottleSnapshot()
	if rem2 < 5*time.Second {
		t.Errorf("remaining after shorter noteThrottle = %v, want > 5s (longest wins)", rem2)
	}
	if rem2 > tot2 {
		t.Errorf("remaining %v > total %v after shorter noteThrottle — torn read", rem2, tot2)
	}

	// clearThrottleState must zero everything.
	clearThrottleState()
	_, remZ, totZ, attemptZ := ThrottleSnapshot()
	if remZ != 0 || totZ != 0 || attemptZ != 0 {
		t.Errorf("after clear: remaining=%v total=%v attempt=%d, want all zero", remZ, totZ, attemptZ)
	}
}

// TestMaxThrottleRetries verifies the retry cap was raised to 8.
func TestMaxThrottleRetries(t *testing.T) {
	if got := MaxThrottleRetries(); got != 8 {
		t.Errorf("MaxThrottleRetries() = %d, want 8", got)
	}
}
