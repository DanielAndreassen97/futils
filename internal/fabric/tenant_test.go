// Tests for tenant binding in the sign-in layer: which tenant values are
// accepted, which login URL they produce, how the tenant list is fetched and
// how a customer is bound to a tenant.
//
// What it protects: a customer bound to a tenant always signs in to that
// tenant, and a value that is not a tenant never reaches a login URL.
//
// Why we need it: these paths call Microsoft in production and cannot be run
// in CI. If they regress, a guest account is signed in to its own tenant
// again and sees another company's workspaces with no error, or a hand-edited
// config value rewrites the login URL.
//
// Example: binding "Contoso" to contoso.no sends the token request to
// login.microsoftonline.com/contoso.no/ and stores the tenant ID read from
// the token, while typing "common" as a tenant is refused.
package fabric

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

const (
	tenantContoso  = "11111111-2222-3333-4444-555555555555"
	tenantFabrikam = "99999999-8888-7777-6666-555555555555"
)

// fakeAccessToken builds a token shaped like the ones Entra issues: three
// base64url parts, the middle one a JSON object carrying the tenant ID.
func fakeAccessToken(tenantID string) string {
	part := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return part(`{"alg":"none"}`) + "." + part(`{"tid":"`+tenantID+`","aud":"fabric"}`) + ".sig"
}

func TestValidateTenant(t *testing.T) {
	accepted := map[string]string{
		tenantContoso:                  tenantContoso,
		strings.ToUpper(tenantContoso): tenantContoso,
		"  contoso.no ":                "contoso.no",
		"Fabrikam.OnMicrosoft.com":     "fabrikam.onmicrosoft.com",
		"sub-domain.contoso.co.uk":     "sub-domain.contoso.co.uk",
	}
	for in, want := range accepted {
		got, err := ValidateTenant(in)
		if err != nil || got != want {
			t.Errorf("ValidateTenant(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	refused := []string{
		"", "   ", "common", "organizations", "consumers", "contoso",
		"contoso.no/x", "a b.no", "contoso.no?x=1", "../x.no", "contoso..no",
		"-contoso.no", "contoso.no#frag", "11111111-2222-3333-4444",
	}
	for _, in := range refused {
		if got, err := ValidateTenant(in); err == nil {
			t.Errorf("ValidateTenant(%q) accepted it as %q", in, got)
		}
	}
}

func TestAuthorityURL(t *testing.T) {
	cases := []struct{ tenant, endpoint, want string }{
		{"", "token", "https://login.microsoftonline.com/common/oauth2/v2.0/token"},
		{"", "authorize", "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"},
		{tenantContoso, "token", "https://login.microsoftonline.com/" + tenantContoso + "/oauth2/v2.0/token"},
		{"contoso.no", "authorize", "https://login.microsoftonline.com/contoso.no/oauth2/v2.0/authorize"},
	}
	for _, c := range cases {
		got, err := authorityURL(c.tenant, c.endpoint)
		if err != nil || got != c.want {
			t.Errorf("authorityURL(%q, %q) = %q, %v; want %q", c.tenant, c.endpoint, got, err, c.want)
		}
	}
}

// A bad tenant must be an error. Falling back to /common/ would sign a bound
// customer in to the account's own tenant without saying so.
func TestAuthorityURLRefusesBadTenant(t *testing.T) {
	for _, tenant := range []string{"common", "contoso.no/../x", "not a tenant"} {
		if got, err := authorityURL(tenant, "token"); err == nil {
			t.Errorf("authorityURL(%q) = %q, want an error", tenant, got)
		}
	}
	if got, err := authorityURL("", "devicecode"); err == nil {
		t.Errorf("authorityURL with an unknown endpoint = %q, want an error", got)
	}
}

func TestTenantIDFromToken(t *testing.T) {
	if got, ok := tenantIDFromToken(fakeAccessToken(tenantContoso)); !ok || got != tenantContoso {
		t.Errorf("got %q ok=%v, want %q", got, ok, tenantContoso)
	}
	unreadable := []string{
		"", "opaque-token", "a.b", "a.!!!.c",
		base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"aud":"x"}`)) + ".s",
		base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"tid":"contoso.no"}`)) + ".s",
	}
	for _, tok := range unreadable {
		if got, ok := tenantIDFromToken(tok); ok {
			t.Errorf("tenantIDFromToken(%q) = %q, want not readable", tok, got)
		}
	}
}

func TestExplainTenantError(t *testing.T) {
	notFound := explainTenantError("contosso.no", errors.New("invalid_request: AADSTS90002: Tenant 'contosso.no' not found."))
	if !strings.Contains(notFound.Error(), "does not know a tenant called contosso.no") {
		t.Errorf("tenant-not-found not explained: %v", notFound)
	}
	for _, code := range []string{"AADSTS50020", "AADSTS90072"} {
		got := explainTenantError("contoso.no", errors.New("invalid_grant: "+code+": User account does not exist in tenant."))
		if !strings.Contains(got.Error(), "has no access to contoso.no") {
			t.Errorf("%s not explained: %v", code, got)
		}
	}
	other := errors.New("invalid_grant: AADSTS50076: multi-factor authentication required")
	if got := explainTenantError("contoso.no", other); got.Error() != other.Error() {
		t.Errorf("an unknown reply must pass through unchanged, got %v", got)
	}
	if explainTenantError("contoso.no", nil) != nil {
		t.Error("a nil error must stay nil")
	}
}

// stubArmGet replaces the Azure Resource Manager call for one test and
// records every URL requested.
func stubArmGet(t *testing.T, reply func(rawURL, token string) ([]byte, error)) *[]string {
	t.Helper()
	var urls []string
	orig := armGet
	armGet = func(rawURL, token string) ([]byte, error) {
		urls = append(urls, rawURL)
		return reply(rawURL, token)
	}
	t.Cleanup(func() { armGet = orig })
	return &urls
}

func signedIn(t *testing.T, customer string) {
	t.Helper()
	keyring.MockInit()
	saveTokens(customer, tokenResponse{AccessToken: "old-access", RefreshToken: "rt-1", ExpiresIn: 3600})
}

func cachedAccess(customer string) string {
	tok, _ := keyring.Get(keyringService, keyFor(customer, "access_token"))
	return tok
}

func TestListTenants_FollowsPagesAndSorts(t *testing.T) {
	signedIn(t, "Contoso")
	endpoints := stubTokenPost(t, func(_ string, data url.Values) (tokenResponse, error) {
		if data.Get("scope") != armScope || data.Get("refresh_token") != "rt-1" {
			t.Errorf("unexpected grant for the tenant list: %v", data)
		}
		return tokenResponse{AccessToken: "arm-token", RefreshToken: "rt-arm"}, nil
	})
	const page2 = "https://management.azure.com/tenants?api-version=2022-12-01&page=2"
	urls := stubArmGet(t, func(rawURL, token string) ([]byte, error) {
		if token != "arm-token" {
			t.Errorf("tenant list requested with token %q", token)
		}
		if rawURL == page2 {
			return []byte(`{"value":[{"tenantId":"` + tenantContoso + `","displayName":"Contoso","defaultDomain":"contoso.no"}]}`), nil
		}
		return []byte(`{"value":[{"tenantId":"` + strings.ToUpper(tenantFabrikam) + `","displayName":"","defaultDomain":"fabrikam.onmicrosoft.com"}],"nextLink":"` + page2 + `"}`), nil
	})

	got, err := ListTenants("Contoso")
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	want := []Tenant{
		{ID: tenantContoso, DisplayName: "Contoso", DefaultDomain: "contoso.no"},
		{ID: tenantFabrikam, DefaultDomain: "fabrikam.onmicrosoft.com"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("tenants = %+v, want %+v (sorted by domain, IDs lower-cased)", got, want)
	}
	if len(*urls) != 2 || (*urls)[0] != armTenantsURL || (*urls)[1] != page2 {
		t.Errorf("requested %v", *urls)
	}
	if len(*endpoints) != 1 || (*endpoints)[0] != "https://login.microsoftonline.com/common/oauth2/v2.0/token" {
		t.Errorf("the tenant-list grant went to %v", *endpoints)
	}
	// The Resource Manager grant must leave the Fabric sign-in alone.
	if cachedAccess("Contoso") != "old-access" {
		t.Error("listing tenants overwrote the cached Fabric token")
	}
	if rt, _ := keyring.Get(keyringService, keyFor("Contoso", "refresh_token")); rt != "rt-1" {
		t.Errorf("listing tenants replaced the stored refresh token with %q", rt)
	}
}

func TestListTenants_StopsAtPageLimit(t *testing.T) {
	signedIn(t, "Contoso")
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{AccessToken: "arm-token"}, nil
	})
	n := 0
	urls := stubArmGet(t, func(string, string) ([]byte, error) {
		n++
		return []byte(fmt.Sprintf(`{"value":[{"tenantId":"00000000-0000-0000-0000-%012d","defaultDomain":"t%d.example"}],"nextLink":"https://management.azure.com/tenants?page=%d"}`, n, n, n+1)), nil
	})
	got, err := ListTenants("Contoso")
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(*urls) != maxTenantPages || len(got) != maxTenantPages {
		t.Errorf("fetched %d pages and %d tenants, want %d of each", len(*urls), len(got), maxTenantPages)
	}
}

// The bearer token must only ever be sent to Azure Resource Manager, whatever
// a reply's nextLink says.
func TestListTenants_DoesNotFollowLinkToAnotherHost(t *testing.T) {
	signedIn(t, "Contoso")
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{AccessToken: "arm-token"}, nil
	})
	urls := stubArmGet(t, func(string, string) ([]byte, error) {
		return []byte(`{"value":[{"tenantId":"` + tenantContoso + `","defaultDomain":"contoso.no"}],"nextLink":"https://evil.example/tenants"}`), nil
	})
	got, err := ListTenants("Contoso")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListTenants = %+v, %v", got, err)
	}
	if len(*urls) != 1 {
		t.Errorf("followed a link off Azure Resource Manager: %v", *urls)
	}
}

func TestListTenants_Failures(t *testing.T) {
	keyring.MockInit()
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called with no sign-in stored")
		return tokenResponse{}, nil
	})
	if _, err := ListTenants("never-signed-in"); !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}

	signedIn(t, "Contoso")
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{AccessToken: "arm-token"}, nil
	})
	stubArmGet(t, func(string, string) ([]byte, error) { return nil, errors.New("HTTP 403") })
	if _, err := ListTenants("Contoso"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want the Resource Manager failure", err)
	}
}

// The happy path the live probe showed: the sign-in futils already holds is
// accepted at the customer's tenant, with no browser.
func TestBindTenant_SilentGrant(t *testing.T) {
	signedIn(t, "Contoso")
	noBrowser(t)
	fresh := fakeAccessToken(tenantContoso)
	endpoints := stubTokenPost(t, func(_ string, data url.Values) (tokenResponse, error) {
		if data.Get("scope") != scope {
			t.Errorf("bind requested scope %q", data.Get("scope"))
		}
		return tokenResponse{AccessToken: fresh, RefreshToken: "rt-2", ExpiresIn: 3600}, nil
	})

	b, err := BindTenant("Contoso", " Contoso.NO ")
	if err != nil {
		t.Fatalf("BindTenant: %v", err)
	}
	// A typed domain ends up as the tenant's ID, read from the token.
	if b.Token != fresh || b.TenantID != tenantContoso {
		t.Errorf("binding = %+v", b)
	}
	if want := "https://login.microsoftonline.com/contoso.no/oauth2/v2.0/token"; len(*endpoints) != 1 || (*endpoints)[0] != want {
		t.Errorf("bind went to %v, want %s", *endpoints, want)
	}
	if tok, ok := loadCachedToken("Contoso", tenantContoso); !ok || tok != fresh {
		t.Error("the token from the new tenant was not cached")
	}
}

// A mistyped tenant must leave the customer exactly as it was, and must not
// open a browser that can only fail the same way.
func TestBindTenant_UnknownTenantStoresNothing(t *testing.T) {
	signedIn(t, "Contoso")
	noBrowser(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_request", ErrorDesc: "AADSTS90002: Tenant 'contosso.no' not found."}, nil
	})
	_, err := BindTenant("Contoso", "contosso.no")
	if err == nil || !strings.Contains(err.Error(), "does not know a tenant called contosso.no") {
		t.Errorf("err = %v", err)
	}
	if cachedAccess("Contoso") != "old-access" {
		t.Error("a failed bind changed the cached token")
	}
}

func stubBrowser(t *testing.T, reply func(tenant string) (tokenResponse, error)) *[]string {
	t.Helper()
	var tenants []string
	orig := browserSignIn
	browserSignIn = func(tenant string) (tokenResponse, error) {
		tenants = append(tenants, tenant)
		return reply(tenant)
	}
	t.Cleanup(func() { browserSignIn = orig })
	return &tenants
}

func captureNotice(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	orig := authNotice
	authNotice = &b
	t.Cleanup(func() { authNotice = orig })
	return &b
}

func TestBindTenant_RefusedGrantOpensBrowserAtThatTenant(t *testing.T) {
	signedIn(t, "Contoso")
	notice := captureNotice(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_grant", ErrorDesc: "AADSTS50076: MFA required."}, nil
	})
	fresh := fakeAccessToken(tenantContoso)
	opened := stubBrowser(t, func(string) (tokenResponse, error) {
		return tokenResponse{AccessToken: fresh, RefreshToken: "rt-9", ExpiresIn: 3600}, nil
	})

	b, err := BindTenant("Contoso", tenantContoso)
	if err != nil || b.TenantID != tenantContoso || b.Token != fresh {
		t.Fatalf("BindTenant = %+v, %v", b, err)
	}
	if len(*opened) != 1 || (*opened)[0] != tenantContoso {
		t.Errorf("browser opened against %v", *opened)
	}
	if !strings.Contains(notice.String(), "AADSTS50076") {
		t.Errorf("no reason printed before the browser opened: %q", notice.String())
	}
}

func TestBindTenant_NoStoredSignInOpensBrowserWithoutAReason(t *testing.T) {
	keyring.MockInit()
	notice := captureNotice(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called with no sign-in stored")
		return tokenResponse{}, nil
	})
	fresh := fakeAccessToken(tenantContoso)
	opened := stubBrowser(t, func(string) (tokenResponse, error) {
		return tokenResponse{AccessToken: fresh, RefreshToken: "rt-9", ExpiresIn: 3600}, nil
	})
	if b, err := BindTenant("Contoso", "contoso.no"); err != nil || b.TenantID != tenantContoso {
		t.Fatalf("BindTenant = %+v, %v", b, err)
	}
	if len(*opened) != 1 || (*opened)[0] != "contoso.no" {
		t.Errorf("browser opened against %v", *opened)
	}
	if notice.Len() != 0 {
		t.Errorf("printed %q although nothing was refused", notice.String())
	}
}

func TestBindTenant_FailedBrowserSignInStoresNothing(t *testing.T) {
	signedIn(t, "Contoso")
	captureNotice(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{Error: "invalid_grant", ErrorDesc: "AADSTS50076: MFA required."}, nil
	})
	stubBrowser(t, func(string) (tokenResponse, error) {
		return tokenResponse{}, errors.New("authentication timed out after 2 minutes")
	})
	if _, err := BindTenant("Contoso", tenantContoso); err == nil {
		t.Fatal("expected the browser failure")
	}
	if cachedAccess("Contoso") != "old-access" {
		t.Error("a failed bind changed the cached token")
	}
}

// When the token cannot be read, an ID the user picked is still the tenant
// whose token URL issued the token, so it stands. A domain has no ID to fall
// back on, so the bind is refused rather than storing something unstable.
func TestBindTenant_UnreadableToken(t *testing.T) {
	signedIn(t, "Contoso")
	noBrowser(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		return tokenResponse{AccessToken: "opaque-token", ExpiresIn: 3600}, nil
	})

	if b, err := BindTenant("Contoso", tenantContoso); err != nil || b.TenantID != tenantContoso {
		t.Errorf("picked ID: BindTenant = %+v, %v", b, err)
	}

	signedIn(t, "Contoso")
	_, err := BindTenant("Contoso", "contoso.no")
	if err == nil || !strings.Contains(err.Error(), "tenant ID instead") {
		t.Errorf("typed domain: err = %v", err)
	}
	if cachedAccess("Contoso") != "old-access" {
		t.Error("a refused bind changed the cached token")
	}
}

func TestBindTenant_RejectsValueThatIsNotATenant(t *testing.T) {
	signedIn(t, "Contoso")
	noBrowser(t)
	stubTokenPost(t, func(string, url.Values) (tokenResponse, error) {
		t.Error("token endpoint called for a value that is not a tenant")
		return tokenResponse{}, nil
	})
	if _, err := BindTenant("Contoso", "common"); err == nil {
		t.Error("\"common\" was accepted as a tenant")
	}
}
