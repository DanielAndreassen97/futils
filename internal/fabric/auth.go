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
const (
	authorizeURL = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	tokenURL     = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	clientID     = "1950a258-227b-4e31-a9cf-717495945fc2"
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

func loadCachedToken(profile string) (string, bool) {
	token, err := keyring.Get(keyringService, keyFor(profile, "access_token"))
	if err != nil || token == "" {
		return "", false
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

func refreshAccessToken(profile string) (string, bool) {
	rt, err := keyring.Get(keyringService, keyFor(profile, "refresh_token"))
	if err != nil || rt == "" {
		return "", false
	}
	data := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"scope":         {scope},
	}
	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", false
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return "", false
	}
	saveTokens(profile, tr)
	return tr.AccessToken, true
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

// storageTokenGrant exchanges a refresh token for a Storage-audience access
// token. It does NOT persist anything — persisting would overwrite the cached
// Fabric access token/expiry and break the Fabric token cache.
func storageTokenGrant(refreshToken string) (string, error) {
	tr, err := tokenPost(tokenURL, url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {storageScope},
	})
	if err != nil {
		return "", err
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return "", fmt.Errorf("storage token grant failed: %s — %s", tr.Error, tr.ErrorDesc)
	}
	return tr.AccessToken, nil
}

// GetStorageToken returns an access token for the OneLake Table API (Storage
// audience). Requires a prior interactive Fabric sign-in (so a refresh token
// exists in the keyring); callers should call GetAccessToken first.
func GetStorageToken(profile string) (string, error) {
	rt, err := keyring.Get(keyringService, keyFor(profile, "refresh_token"))
	if err != nil || rt == "" {
		return "", fmt.Errorf("no stored credentials for %q — sign in first", profile)
	}
	return storageTokenGrant(rt)
}

// GetAccessToken returns a Fabric access token for the given profile name,
// using cached/refreshed tokens when possible, falling back to browser auth.
// Pass "default" if you don't need multi-tenant support yet.
//
// As a side effect, the profile is remembered package-wide via SetProfile
// so the HTTP wrappers' 401-retry path can mint a fresh token mid-flow
// without every caller threading the profile through pollers / interfaces.
func GetAccessToken(profile string) (string, error) {
	SetProfile(profile)
	if token, ok := loadCachedToken(profile); ok {
		return token, nil
	}
	if token, ok := refreshAccessToken(profile); ok {
		return token, nil
	}
	return browserAuth(profile)
}

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

func buildAuthorizeURL(redirectURI, state, codeChallenge string) string {
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
	return authorizeURL + "?" + params.Encode()
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

func browserAuth(profile string) (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("failed to start local server: %w", err)
	}
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
		return "", err
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return "", err
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

	authURL := buildAuthorizeURL(redirectURI, state, challenge)
	fmt.Fprintln(os.Stderr, "Opening browser for Microsoft login…")
	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "Couldn't open browser automatically. Open this URL manually:\n%s\n", authURL)
	}

	select {
	case code := <-codeCh:
		return exchangeCode(profile, code, redirectURI, verifier)
	case err := <-errCh:
		return "", err
	case <-time.After(2 * time.Minute):
		return "", fmt.Errorf("authentication timed out after 2 minutes")
	}
}

func exchangeCode(profile, code, redirectURI, verifier string) (string, error) {
	data := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"scope":         {scope},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("auth error: %s — %s", tr.Error, tr.ErrorDesc)
	}
	saveTokens(profile, tr)
	return tr.AccessToken, nil
}
