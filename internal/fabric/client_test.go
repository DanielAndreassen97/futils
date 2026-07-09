package fabric

import (
	"encoding/json"
	"errors"
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
	urls      []string // full request URL per call
	bodies    [][]byte // request body bytes per call (nil if no body)
}

type seqResponse struct {
	status     int
	retryAfter string
	location   string // sets the Location header (for 202 LRO responses)
	body       string
	err        error // when set, RoundTrip returns this transport error instead of a response
}

func (s *seqTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, req.Header.Get("Authorization"))
	s.urls = append(s.urls, req.URL.String())
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.bodies = append(s.bodies, b)
	} else {
		s.bodies = append(s.bodies, nil)
	}
	idx := len(s.calls) - 1
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	r := s.responses[idx]
	if r.err != nil {
		return nil, r.err
	}
	hdr := http.Header{}
	if r.retryAfter != "" {
		hdr.Set("Retry-After", r.retryAfter)
	}
	if r.location != "" {
		hdr.Set("Location", r.location)
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
	_, rem, tot, attempt := ThrottleSnapshot()
	if attempt != 2 {
		t.Errorf("ThrottleAttempt() = %d, want 2", attempt)
	}
	if tot != 8*time.Second {
		t.Errorf("ThrottleTotal() = %v, want 8s", tot)
	}
	if rem <= 6*time.Second || rem > 8*time.Second {
		t.Errorf("ThrottleRemaining() = %v, want in (6s, 8s]", rem)
	}

	// Call with shorter backoff — deadline must NOT move earlier
	noteThrottle(2*time.Second, 0)
	_, rem2, _, _ := ThrottleSnapshot()
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

func TestBulkImportDefinitionsSyncSuccess(t *testing.T) {
	respBody := `{"importItemDefinitionsDetails":[
		{"itemId":"id-1","itemDisplayName":"MyReport","itemType":"Report","itemLogicalId":"lg-1","operationType":"Create","operationStatus":"Succeeded"},
		{"itemId":"id-2","itemDisplayName":"MyModel","itemType":"SemanticModel","itemLogicalId":"lg-2","operationType":"Update","operationStatus":"SucceededDespiteFailures"}
	]}`
	transport := &seqTransport{responses: []seqResponse{{status: 200, body: respBody}}}

	origClient := httpClient
	origRetry := retryTokenFn
	t.Cleanup(func() {
		httpClient = origClient
		retryTokenFn = origRetry
	})
	httpClient = &http.Client{Transport: transport}
	retryTokenFn = func() (string, bool) { return "fresh-token", true }

	parts := []DefinitionPart{
		{Path: "/A.Report/.platform", Payload: "eyJhIjoxfQ==", PayloadType: "InlineBase64"},
	}
	res, err := BulkImportDefinitions("tok", "11111111-1111-1111-1111-111111111111", parts, BulkImportOptions{AllowPairingByName: true})
	if err != nil {
		t.Fatalf("BulkImportDefinitions returned error: %v", err)
	}
	if len(res.Details) != 2 {
		t.Fatalf("want 2 details, got %d", len(res.Details))
	}
	if res.Details[0].OperationType != "Create" || res.Details[0].ItemID != "id-1" {
		t.Errorf("detail[0] = %+v", res.Details[0])
	}
	if res.Details[1].OperationStatus != "SucceededDespiteFailures" {
		t.Errorf("detail[1] status = %q, want SucceededDespiteFailures", res.Details[1].OperationStatus)
	}
	// Exactly one HTTP call (synchronous 200, no polling).
	if len(transport.calls) != 1 {
		t.Fatalf("want 1 HTTP call, got %d", len(transport.calls))
	}

	// The bulk URL must include ?beta=true.
	if !strings.Contains(transport.urls[0], "?beta=true") {
		t.Errorf("bulk import URL missing ?beta=true: %q", transport.urls[0])
	}

	// The request body must contain definitionParts (len 1) and options.allowPairingByName=true.
	var sentBody struct {
		DefinitionParts []json.RawMessage `json:"definitionParts"`
		Options         struct {
			AllowPairingByName bool `json:"allowPairingByName"`
		} `json:"options"`
	}
	if err := json.Unmarshal(transport.bodies[0], &sentBody); err != nil {
		t.Fatalf("could not unmarshal sent request body: %v", err)
	}
	if len(sentBody.DefinitionParts) != 1 {
		t.Errorf("want 1 definitionPart in request body, got %d", len(sentBody.DefinitionParts))
	}
	if !sentBody.Options.AllowPairingByName {
		t.Errorf("want options.allowPairingByName=true in request body, got false")
	}
}

// TestUpdateItemDefinitionAsyncNoResult verifies that an UpdateItemDefinition
// whose LRO goes async (202 → poll → Succeeded) succeeds even though the
// operation produces no result. Fabric returns 400 OperationHasNoResult from
// the /result endpoint for update operations (per the LRO contract, "not all
// long running operations have a result"); the client must treat that as
// success, not a deploy failure. This reproduces the "Update Report:
// OperationHasNoResult" failure seen deploying a report whose definition is
// large enough to take the async path.
func TestUpdateItemDefinitionAsyncNoResult(t *testing.T) {
	transport := &seqTransport{responses: []seqResponse{
		{status: 202, location: "https://api.fabric.microsoft.com/v1/operations/op-123"},
		{status: 200, body: `{"status":"Succeeded"}`},
		{status: 400, body: `{"requestId":"r","errorCode":"OperationHasNoResult","message":"The operation has no result","isRetriable":false}`},
	}}

	origClient := httpClient
	origRetry := retryTokenFn
	t.Cleanup(func() {
		httpClient = origClient
		retryTokenFn = origRetry
	})
	httpClient = &http.Client{Transport: transport}
	retryTokenFn = func() (string, bool) { return "fresh-token", true }

	def := &Definition{Parts: []DefinitionPart{
		{Path: "definition.pbir", Payload: "e30=", PayloadType: "InlineBase64"},
	}}
	err := UpdateItemDefinition("tok",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222", def)
	if err != nil {
		t.Fatalf("UpdateItemDefinition errored on a no-result operation: %v", err)
	}
	// POST + one poll + one /result fetch = 3 calls.
	if len(transport.calls) != 3 {
		t.Fatalf("want 3 HTTP calls (POST, poll, result), got %d: %v", len(transport.calls), transport.urls)
	}
	if !strings.HasSuffix(transport.urls[2], "/result") {
		t.Errorf("third call should be the /result fetch, got %q", transport.urls[2])
	}
}

// TestDoGetRetriesTransientNetworkError: GET is idempotent, so a transport-level
// failure (e.g. "connection reset by peer" mid-poll) must be retried, not returned
// fatally. Two resets then a 200 → doGet succeeds after 3 calls.
func TestDoGetRetriesTransientNetworkError(t *testing.T) {
	transport := &seqTransport{responses: []seqResponse{
		{err: errors.New("read: connection reset by peer")},
		{err: errors.New("read: connection reset by peer")},
		{status: http.StatusOK, body: `"ok"`},
	}}
	origClient, origSleep := httpClient, sleep
	t.Cleanup(func() { httpClient = origClient; sleep = origSleep })
	httpClient = &http.Client{Transport: transport}
	sleep = func(time.Duration) {}

	body, err := doGet("tok", "http://example.invalid/api")
	if err != nil {
		t.Fatalf("doGet should retry transient network errors, got: %v", err)
	}
	if string(body) != `"ok"` {
		t.Fatalf("body = %q, want \"ok\"", string(body))
	}
	if len(transport.calls) != 3 {
		t.Fatalf("expected 3 calls (2 retries then success), got %d", len(transport.calls))
	}
}

// TestDoGetGivesUpAfterMaxNetRetries: a persistent transport error is retried a
// bounded number of times, then returned — no infinite loop.
func TestDoGetGivesUpAfterMaxNetRetries(t *testing.T) {
	transport := &seqTransport{responses: []seqResponse{
		{err: errors.New("read: connection reset by peer")}, // repeats (last entry replays)
	}}
	origClient, origSleep := httpClient, sleep
	t.Cleanup(func() { httpClient = origClient; sleep = origSleep })
	httpClient = &http.Client{Transport: transport}
	sleep = func(time.Duration) {}

	if _, err := doGet("tok", "http://example.invalid/api"); err == nil {
		t.Fatal("doGet should give up and return after maxNetRetries")
	}
	if len(transport.calls) != maxNetRetries+1 {
		t.Fatalf("expected %d calls (initial + %d retries), got %d", maxNetRetries+1, maxNetRetries, len(transport.calls))
	}
}
