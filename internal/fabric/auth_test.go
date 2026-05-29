package fabric

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestEffectiveTokenExpiry_GuardsZeroAndNegative(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	hour := base.Add(time.Hour)

	cases := []struct {
		name      string
		expiresIn int
		want      time.Time
	}{
		{"normal one hour", 3600, hour},
		{"zero falls back to one hour", 0, hour},
		{"negative falls back to one hour", -5, hour},
		{"shorter than default is honoured", 300, base.Add(5 * time.Minute)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveTokenExpiry(tc.expiresIn, base)
			if !got.Equal(tc.want) {
				t.Errorf("ExpiresIn=%d: got %v, want %v", tc.expiresIn, got, tc.want)
			}
		})
	}
}

// TestClearCachedTokens_SurfacesKeyringError pins the security-relevant
// contract that a failed keyring delete is NOT silently swallowed: logout
// must be able to tell the user the wipe didn't fully succeed rather than
// printing a false "cleared".
func TestClearCachedTokens_SurfacesKeyringError(t *testing.T) {
	boom := errors.New("keyring locked")
	keyring.MockInitWithError(boom)
	t.Cleanup(keyring.MockInit) // reset provider for later tests

	err := ClearCachedTokens("acme")
	if err == nil {
		t.Fatal("expected an error when keyring deletes fail, got nil")
	}
	if !strings.Contains(err.Error(), "keyring locked") {
		t.Errorf("error should wrap the keyring failure, got %v", err)
	}
}

// TestClearCachedTokens_AbsentTokensIsSuccess: deleting tokens that were
// never stored is the normal case (the "default" profile, a customer that
// never authenticated). keyring.ErrNotFound must be treated as success, not
// surfaced as a spurious failure.
func TestClearCachedTokens_AbsentTokensIsSuccess(t *testing.T) {
	keyring.MockInit()
	if err := ClearCachedTokens("never-logged-in"); err != nil {
		t.Errorf("clearing absent tokens should succeed, got %v", err)
	}
}

// TestTokenCacheRoundTrip exercises the save → load → clear lifecycle end
// to end against a mock keyring — previously zero-coverage paths.
func TestTokenCacheRoundTrip(t *testing.T) {
	keyring.MockInit()

	saveTokens("acme", tokenResponse{AccessToken: "tok-123", RefreshToken: "ref-456", ExpiresIn: 3600})

	tok, ok := loadCachedToken("acme")
	if !ok || tok != "tok-123" {
		t.Fatalf("expected cached token tok-123, got %q ok=%v", tok, ok)
	}

	if err := ClearCachedTokens("acme"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if _, ok := loadCachedToken("acme"); ok {
		t.Error("token still cached after ClearCachedTokens")
	}
}

// TestLoadCachedToken_RespectsExpirySkew pins the 5-minute negative skew in
// loadCachedToken: a token with less than 300s of life left must be treated
// as stale so callers refresh proactively rather than handing out a token
// that expires mid-operation.
func TestLoadCachedToken_RespectsExpirySkew(t *testing.T) {
	keyring.MockInit()

	saveTokens("short", tokenResponse{AccessToken: "x", ExpiresIn: 60})
	if _, ok := loadCachedToken("short"); ok {
		t.Error("token with 60s life should be considered stale within the 300s skew window")
	}

	saveTokens("long", tokenResponse{AccessToken: "y", ExpiresIn: 3600})
	if _, ok := loadCachedToken("long"); !ok {
		t.Error("token with 1h life should be valid")
	}
}

// TestPKCEPair verifies the PKCE S256 invariant: the challenge is the
// base64url-encoded SHA256 of the verifier, and the verifier length stays
// inside RFC 7636's 43-128 character range. A regression here (e.g. a
// shortened verifier or a wrong hash) would weaken the only protection
// against authorization-code interception on the loopback redirect.
func TestPKCEPair(t *testing.T) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		t.Fatalf("pkcePair returned error: %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length %d outside RFC 7636 range 43-128", len(verifier))
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Errorf("challenge is not S256(verifier): got %q want %q", challenge, want)
	}

	// Two pairs must differ — a constant verifier would be a public secret.
	v2, _, _ := pkcePair()
	if verifier == v2 {
		t.Error("two pkcePair calls produced the same verifier")
	}
}

// TestRandomState pins that the CSRF state nonce is 128 bits of entropy
// (16 bytes → 32 hex chars) and is non-deterministic.
func TestRandomState(t *testing.T) {
	s1, err := randomState()
	if err != nil {
		t.Fatalf("randomState error: %v", err)
	}
	if len(s1) != 32 {
		t.Errorf("expected 32 hex chars (16 bytes), got %d", len(s1))
	}
	s2, _ := randomState()
	if s1 == s2 {
		t.Error("randomState returned identical values across calls")
	}
}

// TestBuildAuthorizeURL asserts the authorization request carries the
// security-critical parameters: S256 PKCE method, the exact state and
// challenge passed in, response_type=code, and the loopback redirect URI.
func TestBuildAuthorizeURL(t *testing.T) {
	raw := buildAuthorizeURL("http://127.0.0.1:5000", "the-state", "the-challenge")
	if !strings.HasPrefix(raw, authorizeURL+"?") {
		t.Fatalf("authorize URL does not start with the AAD authorize endpoint: %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("authorize URL did not parse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":         "code",
		"code_challenge_method": "S256",
		"state":                 "the-state",
		"code_challenge":        "the-challenge",
		"client_id":             clientID,
		"redirect_uri":          "http://127.0.0.1:5000",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query %q: got %q, want %q", k, got, want)
		}
	}
}
