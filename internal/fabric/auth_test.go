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

	tok, ok := loadCachedToken("acme", "")
	if !ok || tok != "tok-123" {
		t.Fatalf("expected cached token tok-123, got %q ok=%v", tok, ok)
	}

	if err := ClearCachedTokens("acme"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if _, ok := loadCachedToken("acme", ""); ok {
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
	if _, ok := loadCachedToken("short", ""); ok {
		t.Error("token with 60s life should be considered stale within the 300s skew window")
	}

	saveTokens("long", tokenResponse{AccessToken: "y", ExpiresIn: 3600})
	if _, ok := loadCachedToken("long", ""); !ok {
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
	raw, err := buildAuthorizeURL("", "http://localhost:5000", "the-state", "the-challenge")
	if err != nil {
		t.Fatalf("buildAuthorizeURL: %v", err)
	}
	// An unbound customer must keep the exact endpoint it used before tenants existed.
	if !strings.HasPrefix(raw, "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?") {
		t.Fatalf("authorize URL does not start with the /common/ authorize endpoint: %q", raw)
	}
	bound, err := buildAuthorizeURL(tenantContoso, "http://localhost:5000", "the-state", "the-challenge")
	if err != nil {
		t.Fatalf("buildAuthorizeURL with tenant: %v", err)
	}
	if !strings.HasPrefix(bound, "https://login.microsoftonline.com/"+tenantContoso+"/oauth2/v2.0/authorize?") {
		t.Fatalf("bound customer's authorize URL does not target its tenant: %q", bound)
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
		"redirect_uri":          "http://localhost:5000",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query %q: got %q, want %q", k, got, want)
		}
	}
}

// stubTokenPost replaces the token endpoint call for one test and records the
// endpoint each request went to.
func stubTokenPost(t *testing.T, reply func(endpoint string, data url.Values) (tokenResponse, error)) *[]string {
	t.Helper()
	var endpoints []string
	orig := tokenPost
	tokenPost = func(endpoint string, data url.Values) (tokenResponse, error) {
		endpoints = append(endpoints, endpoint)
		return reply(endpoint, data)
	}
	t.Cleanup(func() { tokenPost = orig })
	return &endpoints
}

// noBrowser fails the test if a flow falls through to the browser sign-in.
func noBrowser(t *testing.T) {
	t.Helper()
	orig := browserSignIn
	browserSignIn = func(tenant string) (tokenResponse, error) {
		t.Errorf("browser sign-in opened (tenant %q), expected a silent path", tenant)
		return tokenResponse{}, errors.New("browser not expected")
	}
	t.Cleanup(func() { browserSignIn = orig })
}

func resolveTo(t *testing.T, tenant Tenant, err error) {
	t.Helper()
	SetTenantResolver(func(string) (Tenant, error) { return tenant, err })
	// GetAccessToken also records the profile for the 401 retry; reset both so
	// no later test inherits this customer.
	t.Cleanup(func() { SetTenantResolver(nil); SetProfile("") })
}

// A bound customer must never be handed a cached token that another tenant
// issued: that is the wrong-workspaces failure tenant binding exists to stop.
func TestLoadCachedToken_BoundCustomerRejectsOtherTenantToken(t *testing.T) {
	keyring.MockInit()
	saveTokens("Contoso", tokenResponse{AccessToken: fakeAccessToken(tenantFabrikam), ExpiresIn: 3600})

	if _, ok := loadCachedToken("Contoso", tenantContoso); ok {
		t.Error("a token issued by another tenant was served to a bound customer")
	}
	if _, ok := loadCachedToken("Contoso", strings.ToUpper(tenantFabrikam)); !ok {
		t.Error("a token from the customer's own tenant must be served, whatever the case of the ID")
	}
	if _, ok := loadCachedToken("Contoso", ""); !ok {
		t.Error("an unbound customer's cache must not be checked against any tenant")
	}
}

// A token whose tenant cannot be read is not proof of anything, so a bound
// customer refreshes instead of trusting it.
func TestLoadCachedToken_BoundCustomerRejectsUnreadableToken(t *testing.T) {
	keyring.MockInit()
	saveTokens("Contoso", tokenResponse{AccessToken: "opaque-token", ExpiresIn: 3600})
	if _, ok := loadCachedToken("Contoso", tenantContoso); ok {
		t.Error("an unreadable token was served to a bound customer")
	}
}

func TestRefreshUsesTenantEndpoint(t *testing.T) {
	for tenant, want := range map[string]string{
		"":            "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		tenantContoso: "https://login.microsoftonline.com/" + tenantContoso + "/oauth2/v2.0/token",
	} {
		keyring.MockInit()
		if err := keyring.Set(keyringService, keyFor("Contoso", "refresh_token"), "rt-1"); err != nil {
			t.Fatal(err)
		}
		endpoints := stubTokenPost(t, func(_ string, data url.Values) (tokenResponse, error) {
			if data.Get("grant_type") != "refresh_token" || data.Get("refresh_token") != "rt-1" || data.Get("scope") != scope {
				t.Errorf("unexpected refresh request: %v", data)
			}
			return tokenResponse{AccessToken: "fresh", RefreshToken: "rt-2", ExpiresIn: 3600}, nil
		})
		tok, err := refreshAccessToken("Contoso", tenant)
		if err != nil || tok != "fresh" {
			t.Fatalf("tenant %q: refresh = %q, %v", tenant, tok, err)
		}
		if len(*endpoints) != 1 || (*endpoints)[0] != want {
			t.Errorf("tenant %q: refresh went to %v, want %s", tenant, *endpoints, want)
		}
		if rt, _ := keyring.Get(keyringService, keyFor("Contoso", "refresh_token")); rt != "rt-2" {
			t.Errorf("tenant %q: rotated refresh token not saved, got %q", tenant, rt)
		}
	}
}

func TestRefreshWithoutRefreshTokenIsNamedError(t *testing.T) {
	keyring.MockInit()
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called with no refresh token stored")
		return tokenResponse{}, nil
	})
	if _, err := refreshAccessToken("never-signed-in", ""); !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}
}

func TestRefreshSurfacesEntraReason(t *testing.T) {
	keyring.MockInit()
	_ = keyring.Set(keyringService, keyFor("Contoso", "refresh_token"), "rt-1")
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_grant", ErrorDesc: "AADSTS50076: MFA required.\r\nTrace ID: abc"}, nil
	})
	_, err := refreshAccessToken("Contoso", tenantContoso)
	if err == nil || errors.Is(err, ErrNoRefreshToken) || !strings.Contains(err.Error(), "AADSTS50076") {
		t.Errorf("err = %v, want Entra's reason", err)
	}
}

// A config that cannot be read must stop the sign-in. Treating it as "no
// tenant" would sign a bound customer in to the wrong tenant silently.
func TestGetAccessToken_ResolverErrorStopsSignIn(t *testing.T) {
	keyring.MockInit()
	noBrowser(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called although the tenant could not be resolved")
		return tokenResponse{}, nil
	})
	boom := errors.New("config unreadable")
	resolveTo(t, Tenant{}, boom)
	if _, err := GetAccessToken("Contoso"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the resolver's error", err)
	}
}

// The whole point of binding: a cached token from the account's own tenant is
// skipped, and the refresh goes to the customer's tenant without a browser.
func TestGetAccessToken_BoundCustomerRefreshesAtItsTenant(t *testing.T) {
	keyring.MockInit()
	noBrowser(t)
	notice := captureNotice(t)

	saveTokens("Contoso", tokenResponse{AccessToken: fakeAccessToken(tenantFabrikam), RefreshToken: "rt-1", ExpiresIn: 3600})
	resolveTo(t, Tenant{ID: tenantContoso, DefaultDomain: "contoso.no"}, nil)
	fresh := fakeAccessToken(tenantContoso)
	endpoints := stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{AccessToken: fresh, ExpiresIn: 3600}, nil
	})

	tok, err := GetAccessToken("Contoso")
	if err != nil || tok != fresh {
		t.Fatalf("GetAccessToken = %q, %v", tok, err)
	}
	want := "https://login.microsoftonline.com/" + tenantContoso + "/oauth2/v2.0/token"
	if len(*endpoints) != 1 || (*endpoints)[0] != want {
		t.Errorf("refresh went to %v, want %s", *endpoints, want)
	}
	if !strings.Contains(notice.String(), "Tenant: contoso.no") {
		t.Errorf("the bound tenant was not shown at sign-in, got %q", notice.String())
	}
}

// An unbound customer keeps its cached token and prints nothing new.
func TestGetAccessToken_UnboundCustomerUsesCacheSilently(t *testing.T) {
	keyring.MockInit()
	noBrowser(t)
	notice := captureNotice(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called although a valid token was cached")
		return tokenResponse{}, nil
	})

	t.Cleanup(func() { SetProfile("") })
	saveTokens("Fabrikam", tokenResponse{AccessToken: "cached", RefreshToken: "rt-1", ExpiresIn: 3600})
	tok, err := GetAccessToken("Fabrikam")
	if err != nil || tok != "cached" {
		t.Fatalf("GetAccessToken = %q, %v", tok, err)
	}
	if notice.Len() != 0 {
		t.Errorf("an unbound customer's sign-in printed %q", notice.String())
	}
}

// When the saved sign-in is refused, the browser opens against the customer's
// tenant, and the line printed first names Entra's reason and no guessed cause.
func TestGetAccessToken_BoundCustomerFallsBackToBrowserAtItsTenant(t *testing.T) {
	keyring.MockInit()
	notice := captureNotice(t)

	_ = keyring.Set(keyringService, keyFor("Contoso", "refresh_token"), "rt-1")
	resolveTo(t, Tenant{ID: tenantContoso}, nil)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_grant", ErrorDesc: "AADSTS50076: MFA required."}, nil
	})
	fresh := fakeAccessToken(tenantContoso)
	opened := stubBrowser(t, func(string) (tokenResponse, error) {
		return tokenResponse{AccessToken: fresh, RefreshToken: "rt-9", ExpiresIn: 3600}, nil
	})

	tok, err := GetAccessToken("Contoso")
	if err != nil || tok != fresh {
		t.Fatalf("GetAccessToken = %q, %v", tok, err)
	}
	if len(*opened) != 1 || (*opened)[0] != tenantContoso {
		t.Errorf("browser opened against %v, want the customer's tenant", *opened)
	}
	out := notice.String()
	if !strings.Contains(out, "Could not renew the saved sign-in") || !strings.Contains(out, "AADSTS50076") {
		t.Errorf("no reason printed before the browser opened, got %q", out)
	}
	if cached, ok := loadCachedToken("Contoso", tenantContoso); !ok || cached != fresh {
		t.Error("the token from the browser sign-in was not cached")
	}
}

func TestExchangeCodeUsesTenantEndpoint(t *testing.T) {
	for tenant, want := range map[string]string{
		"":            "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		tenantContoso: "https://login.microsoftonline.com/" + tenantContoso + "/oauth2/v2.0/token",
	} {
		endpoints := stubTokenPost(t, func(_ string, data url.Values) (tokenResponse, error) {
			if data.Get("grant_type") != "authorization_code" || data.Get("code") != "the-code" || data.Get("code_verifier") != "the-verifier" {
				t.Errorf("unexpected code exchange request: %v", data)
			}
			return tokenResponse{AccessToken: "from-code"}, nil
		})
		tr, err := exchangeCode(tenant, "the-code", "http://localhost:5000", "the-verifier")
		if err != nil || tr.AccessToken != "from-code" {
			t.Fatalf("tenant %q: exchangeCode = %+v, %v", tenant, tr, err)
		}
		if len(*endpoints) != 1 || (*endpoints)[0] != want {
			t.Errorf("tenant %q: code exchange went to %v, want %s", tenant, *endpoints, want)
		}
	}
}

// A tenant Entra has never heard of fails the same way in a browser, so the
// sign-in stops with a sentence naming the tenant instead of opening a page
// that can only show an error and then waiting two minutes.
func TestGetAccessToken_UnknownTenantDoesNotOpenBrowser(t *testing.T) {
	keyring.MockInit()
	noBrowser(t)
	captureNotice(t)

	_ = keyring.Set(keyringService, keyFor("Contoso", "refresh_token"), "rt-1")
	resolveTo(t, Tenant{ID: tenantContoso, DefaultDomain: "contoso.no"}, nil)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_request", ErrorDesc: "AADSTS90002: Tenant not found."}, nil
	})

	_, err := GetAccessToken("Contoso")
	if err == nil || !strings.Contains(err.Error(), "does not know a tenant called contoso.no") {
		t.Errorf("err = %v, want the tenant named", err)
	}
}

// In a browser, a wrong account or tenant shows Entra's error page and never
// redirects back, so the only thing futils sees is the timeout. For a bound
// sign-in the timeout must say which tenant it was for.
func TestBrowserTimeoutErrorNamesTheTenant(t *testing.T) {
	if got := browserTimeoutError("").Error(); got != "authentication timed out after 2 minutes" {
		t.Errorf("unbound timeout changed: %q", got)
	}
	if got := browserTimeoutError("contoso.no").Error(); !strings.Contains(got, "timed out") || !strings.Contains(got, "contoso.no") {
		t.Errorf("bound timeout = %q, want the tenant named", got)
	}
}
