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
	if got := throttleDelay("9999", 0); got != maxThrottleWait {
		t.Errorf("got %v, want %v", got, maxThrottleWait)
	}
}

func TestThrottleDelayBackoffWithoutHeader(t *testing.T) {
	if got := throttleDelay("", 2); got != 4*time.Second { // 1<<2
		t.Errorf("got %v, want 4s", got)
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
