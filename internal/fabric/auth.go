package fabric

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// OAuth2 configuration. We reuse the Azure CLI public client ID because it's
// pre-consented for Fabric and PowerBI scopes — no app registration needed.
//
// The authorize and token URLs are built per customer by authorityURL
// (tenant.go): /common/ for an unbound customer, the customer's tenant
// otherwise.
const (
	clientID = "1950a258-227b-4e31-a9cf-717495945fc2"
	// Fabric-native scope. Covers workspaces, items, getDefinition, and the
	// jobs API (RunNotebook) in a single token.
	scope = "https://api.fabric.microsoft.com/.default offline_access"

	keyringService = "futils"
)

func keyFor(profile, kind string) string {
	return profile + ":" + kind
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// keyringWarnOnce ensures the keyring-unavailable warning is printed at
// most once per process — repeated saves shouldn't spam stderr, but the
// first failure should still surface so headless-Linux users know why
// tokens aren't sticking.
var keyringWarnOnce sync.Once

func keyringSet(key, value string) {
	if err := keyring.Set(keyringService, key, value); err != nil {
		keyringWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: OS keyring unavailable (%v) — tokens won't persist across runs\n", err)
		})
	}
}

// effectiveTokenExpiry returns the time at which a token with the given
// ExpiresIn (seconds) should be treated as stale. ExpiresIn <= 0 is
// treated as AAD's documented default (3600s) — without this guard a
// malformed response sets expiry to time.Now() and every subsequent
// loadCachedToken returns false, forcing refresh-token grants on every
// invocation (or worse, force-browser-auth if the refresh token is also
// stale).
func effectiveTokenExpiry(expiresIn int, now time.Time) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

func saveTokens(profile string, tr tokenResponse) {
	expiry := effectiveTokenExpiry(tr.ExpiresIn, time.Now())
	keyringSet(keyFor(profile, "access_token"), tr.AccessToken)
	keyringSet(keyFor(profile, "token_expiry"), strconv.FormatInt(expiry.Unix(), 10))
	if tr.RefreshToken != "" {
		keyringSet(keyFor(profile, "refresh_token"), tr.RefreshToken)
	}
}

// loadCachedToken returns the cached access token while it has more than five
// minutes left. For a customer bound to a tenant it also requires that the
// token was issued by that tenant: a token from any other tenant, or one whose
// tenant cannot be read, is not served. An unbound customer (tenant "") gets
// no such check, so its cache behaves exactly as before tenants existed.
func loadCachedToken(profile, tenant string) (string, bool) {
	token, err := keyring.Get(keyringService, keyFor(profile, "access_token"))
	if err != nil || token == "" {
		return "", false
	}
	if tenant != "" {
		if tid, ok := tenantIDFromToken(token); !ok || !strings.EqualFold(tid, tenant) {
			return "", false
		}
	}
	expiryStr, err := keyring.Get(keyringService, keyFor(profile, "token_expiry"))
	if err != nil {
		return "", false
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix()+300 < expiry {
		return token, true
	}
	return "", false
}

// refreshAccessToken trades the stored refresh token for a new Fabric access
// token at the given tenant ("" = /common/) and caches the result. It returns
// ErrNoRefreshToken when nothing is stored, and otherwise the reason the
// grant failed, so the caller can tell the user why a browser is opening.
func refreshAccessToken(profile, tenant string) (string, error) {
	rt, err := keyring.Get(keyringService, keyFor(profile, "refresh_token"))
	if err != nil || rt == "" {
		return "", ErrNoRefreshToken
	}
	tr, err := tokenGrant(tenant, scope, rt)
	if err != nil {
		return "", err
	}
	saveTokens(profile, tr)
	return tr.AccessToken, nil
}

// storageScope is the OneLake/Storage audience used for the OneLake Table API.
// Distinct from the Fabric scope; minted from the same refresh token.
const storageScope = "https://storage.azure.com/.default offline_access"

// tokenPost is the seam for the token endpoint POST so tests can stub it.
var tokenPost = func(endpoint string, data url.Values) (tokenResponse, error) {
	resp, err := http.PostForm(endpoint, data)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tokenResponse{}, err
	}
	return tr, nil
}

// tokenGrant exchanges a refresh token for an access token of the given scope
// at the given tenant ("" = /common/). It does NOT persist anything: callers
// that want the result cached save it themselves, and the Storage and Azure
// Resource Manager grants must never be saved, because that would overwrite
// the cached Fabric access token and expiry.
func tokenGrant(tenant, scope, refreshToken string) (tokenResponse, error) {
	endpoint, err := authorityURL(tenant, "token")
	if err != nil {
		return tokenResponse{}, err
	}
	tr, err := tokenPost(endpoint, url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {scope},
	})
	if err != nil {
		return tokenResponse{}, err
	}
	return checkTokenResponse(tr)
}

// checkTokenResponse turns an error reply from the token endpoint into a Go
// error. Only the first line of Entra's description is kept: the rest is
// trace IDs and timestamps.
func checkTokenResponse(tr tokenResponse) (tokenResponse, error) {
	if tr.Error != "" {
		desc, _, _ := strings.Cut(tr.ErrorDesc, "\n")
		return tokenResponse{}, fmt.Errorf("%s: %s", tr.Error, strings.TrimSpace(desc))
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, errors.New("token endpoint returned no access token")
	}
	return tr, nil
}

// GetStorageToken returns an access token for the OneLake Table API (Storage
// audience). Requires a prior interactive Fabric sign-in (so a refresh token
// exists in the keyring); callers should call GetAccessToken first.
func GetStorageToken(profile string) (string, error) {
	rt, err := keyring.Get(keyringService, keyFor(profile, "refresh_token"))
	if err != nil || rt == "" {
		return "", fmt.Errorf("no stored credentials for %q — sign in first", profile)
	}
	// Same tenant as the Fabric token: OneLake data lives in the tenant the
	// workspaces do.
	tenant, err := tenantFor(profile)
	if err != nil {
		return "", fmt.Errorf("resolve tenant for %q: %w", profile, err)
	}
	tr, err := tokenGrant(tenant.ID, storageScope, rt)
	if err != nil {
		return "", fmt.Errorf("storage token grant failed: %w", err)
	}
	return tr.AccessToken, nil
}

// GetAccessToken returns a Fabric access token for the given profile name,
// using cached/refreshed tokens when possible, falling back to browser auth.
// Pass "default" if you don't need multi-tenant support yet.
//
// As a side effect, the profile is remembered package-wide via SetProfile
// so the HTTP wrappers' 401-retry path can mint a fresh token mid-flow
// without every caller threading the profile through pollers / interfaces.
//
// A customer bound to a tenant (see SetTenantResolver) gets every step at
// that tenant, and the tenant is printed so a wrong binding is visible
// wherever workspaces are about to be listed. An unbound customer takes the
// exact /common/ path it always has and prints nothing new.
func GetAccessToken(profile string) (string, error) {
	SetProfile(profile)
	tenant, err := tenantFor(profile)
	if err != nil {
		return "", fmt.Errorf("resolve tenant for %q: %w", profile, err)
	}
	if tenant.ID != "" {
		fmt.Fprintf(authNotice, "  Tenant: %s\n", tenant.Label())
	}
	if token, ok := loadCachedToken(profile, tenant.ID); ok {
		return token, nil
	}
	token, err := refreshAccessToken(profile, tenant.ID)
	if err == nil {
		return token, nil
	}
	// An unbound customer goes straight to the browser, as it always has. A
	// bound one is told why first (see browserAfterRefusedGrant).
	var tr tokenResponse
	if tenant.ID == "" {
		tr, err = browserSignIn("")
	} else {
		tr, err = browserAfterRefusedGrant(tenant.ID, tenant.Label(), err)
	}
	if err != nil {
		return "", err
	}
	saveTokens(profile, tr)
	return tr.AccessToken, nil
}

// authNotice is where sign-in lines for bound customers go. A variable so
// tests can capture them.
var authNotice io.Writer = os.Stderr

// browserSignIn runs the interactive browser flow. A variable so tests can
// cover the fallback without opening a browser.
var browserSignIn = browserAuth

// ClearCachedTokens wipes all stored tokens for a profile. It returns a
// non-nil error if any keyring delete fails for a reason other than the
// entry being absent — keyring.ErrNotFound is the normal "nothing to wipe"
// case (the "default" profile, a customer that never authenticated) and is
// treated as success. Surfacing real failures lets logout report honestly
// instead of printing a false "cleared" when the keyring is locked or
// otherwise refuses the delete, leaving a long-lived refresh token behind.
func ClearCachedTokens(profile string) error {
	var errs []error
	for _, kind := range []string{"access_token", "refresh_token", "token_expiry"} {
		if err := keyring.Delete(keyringService, keyFor(profile, kind)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, fmt.Errorf("delete %s: %w", kind, err))
		}
	}
	return errors.Join(errs...)
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes for state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// pkcePair generates a PKCE verifier+challenge pair. Verifier is 32
// bytes of entropy encoded as base64url (≈43 chars, well inside
// RFC 7636's 43-128 range). Challenge is the SHA256 of the verifier,
// base64url encoded — the S256 method.
//
// Without PKCE, anyone who can intercept the authorization code on
// the localhost redirect can exchange it for tokens. The state
// parameter alone only protects against CSRF, not code interception.
//
// On entropy failure we refuse to auth — silently falling back to a
// deterministic all-zero verifier would defeat the whole point of PKCE
// (the verifier would become a public constant any attacker who knows
// this bug could replay).
func pkcePair() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("read random bytes for PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func buildAuthorizeURL(tenant, redirectURI, state, codeChallenge string) (string, error) {
	endpoint, err := authorityURL(tenant, "authorize")
	if err != nil {
		return "", err
	}
	params := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scope},
		"state":                 {state},
		"prompt":                {"select_account"},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	return endpoint + "?" + params.Encode(), nil
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// browserAuth signs in through the browser at the given tenant ("" =
// /common/) and returns the token reply. It stores nothing: the caller saves
// the reply under the right profile.
func browserAuth(tenant string) (tokenResponse, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return tokenResponse{}, fmt.Errorf("failed to start local server: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	// Bind the listener to the IPv4 loopback for a deterministic address,
	// but advertise "localhost" in the redirect URI. AAD only ignores the
	// port component when matching loopback redirects for "localhost"; for a
	// literal 127.0.0.1 the port must match exactly, which is impossible with
	// the ephemeral port above. The Azure CLI public-client app (clientID)
	// also only registers http://localhost, so sending 127.0.0.1 here trips
	// AADSTS50011 (redirect URI mismatch). The browser resolves localhost to
	// 127.0.0.1 and reaches this listener.
	redirectURI := fmt.Sprintf("http://localhost:%d", port)
	state, err := randomState()
	if err != nil {
		return tokenResponse{}, err
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return tokenResponse{}, err
	}
	authURL, err := buildAuthorizeURL(tenant, redirectURI, state, challenge)
	if err != nil {
		return tokenResponse{}, err
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	// Non-blocking sends below — if the buffered channel is already
	// full (a stale browser tab fires a second callback), we drop the
	// duplicate rather than leaking a goroutine blocked on send.
	sendCode := func(c string) {
		select {
		case codeCh <- c:
		default:
		}
	}
	sendErr := func(e error) {
		select {
		case errCh <- e:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			sendErr(fmt.Errorf("state mismatch"))
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			sendErr(fmt.Errorf("auth error: %s — %s", errMsg, r.URL.Query().Get("error_description")))
			safe := strings.ReplaceAll(strings.ReplaceAll(errMsg, "<", "&lt;"), ">", "&gt;")
			// Explicit 4xx so browsers/proxies don't cache the failure
			// page under 200 OK. Mirrors the state-mismatch branch above.
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>%s</p></body></html>", safe)
			return
		}
		sendCode(r.URL.Query().Get("code"))
		fmt.Fprint(w, "<html><body><h2>Authentication successful</h2><p>You can close this tab.</p></body></html>")
	})

	server := &http.Server{Handler: mux}
	// Surface non-shutdown Serve errors via errCh so the select below
	// reports the real cause instead of hanging until the 2-minute
	// timeout. ErrServerClosed is the expected outcome of Shutdown().
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			sendErr(fmt.Errorf("local callback server: %w", err))
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	fmt.Fprintln(os.Stderr, "Opening browser for Microsoft login…")
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "Couldn't open browser automatically. Open this URL manually:\n%s\n", authURL)
	}

	select {
	case code := <-codeCh:
		tr, err := exchangeCode(tenant, code, redirectURI, verifier)
		return tr, explainIfBound(tenant, err)
	case err := <-errCh:
		return tokenResponse{}, explainIfBound(tenant, err)
	case <-time.After(2 * time.Minute):
		return tokenResponse{}, browserTimeoutError(tenant)
	}
}

// explainIfBound applies explainTenantError only when the sign-in targeted a
// tenant, so an unbound customer's errors read exactly as before.
func explainIfBound(tenant string, err error) error {
	if tenant == "" {
		return err
	}
	return explainTenantError(tenant, err)
}

// exchangeCode trades the authorization code from the browser redirect for
// tokens at the given tenant ("" = /common/). It stores nothing.
func exchangeCode(tenant, code, redirectURI, verifier string) (tokenResponse, error) {
	endpoint, err := authorityURL(tenant, "token")
	if err != nil {
		return tokenResponse{}, err
	}
	tr, err := tokenPost(endpoint, url.Values{
		"client_id":     {clientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"scope":         {scope},
		"code_verifier": {verifier},
	})
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token exchange failed: %w", err)
	}
	tr, err = checkTokenResponse(tr)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth error: %w", err)
	}
	return tr, nil
}

// browserTimeoutError is what a browser sign-in returns when no redirect came
// back. A wrong account or tenant shows Entra's error page in the browser and
// never redirects, so for a sign-in at a tenant this is the only place futils
// can say which tenant it was for.
func browserTimeoutError(tenant string) error {
	if tenant == "" {
		return errors.New("authentication timed out after 2 minutes")
	}
	return fmt.Errorf("authentication timed out after 2 minutes. If the browser showed an error, the account may have no access to %s", tenant)
}
