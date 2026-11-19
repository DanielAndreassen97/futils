package fabric

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

// Tenant is one Entra tenant. As a resolver result only ID and DefaultDomain
// are set; ListTenants fills all three.
type Tenant struct {
	ID            string
	DisplayName   string
	DefaultDomain string
}

// Label is what a sign-in line or menu shows for the tenant: its domain when
// one is known, otherwise the start of its ID.
func (t Tenant) Label() string {
	if t.DefaultDomain != "" {
		return t.DefaultDomain
	}
	if len(t.ID) > 8 {
		return t.ID[:8] + "…"
	}
	return t.ID
}

// The tenant resolver is how this package learns which tenant a customer
// signs in to without importing the config package. The token functions take
// only a profile (the customer name), and the 401-retry path has nothing
// else, so the lookup is keyed by that name. It is asked on every token
// request rather than cached, so binding or clearing a tenant in a running
// session can never leave a stale value behind.
var (
	tenantResolverMu sync.RWMutex
	tenantResolver   func(customer string) (Tenant, error)
)

// SetTenantResolver installs the customer → tenant lookup. main.go installs
// one that reads the config; with none installed every customer resolves to
// no tenant, i.e. the /common/ sign-in.
func SetTenantResolver(fn func(customer string) (Tenant, error)) {
	tenantResolverMu.Lock()
	tenantResolver = fn
	tenantResolverMu.Unlock()
}

// tenantFor returns the tenant a customer is bound to; the zero Tenant means
// unbound. An error means the binding could not be read, and callers must
// stop: carrying on as "unbound" would sign a bound customer in to the
// account's own tenant without saying so.
func tenantFor(customer string) (Tenant, error) {
	tenantResolverMu.RLock()
	fn := tenantResolver
	tenantResolverMu.RUnlock()
	if fn == nil {
		return Tenant{}, nil
	}
	return fn(customer)
}

var (
	tenantIDRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	tenantDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// IsTenantID reports whether s is a tenant ID, i.e. a GUID in any case.
func IsTenantID(s string) bool {
	return tenantIDRe.MatchString(strings.ToLower(s))
}

// ValidateTenant accepts a tenant ID (a GUID) or a domain with at least one
// dot, and returns it trimmed and lower-cased. Everything else is refused.
// The value ends up in the path of a login URL, so this is also what keeps
// "common", "organizations" and "consumers" (authority names that would
// quietly switch the binding off) and anything with a slash or a query out.
func ValidateTenant(text string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(text))
	if tenantIDRe.MatchString(v) {
		return v, nil
	}
	if len(v) <= 253 && tenantDomainRe.MatchString(v) {
		return v, nil
	}
	return "", fmt.Errorf("%q is not a tenant ID or a domain", strings.TrimSpace(text))
}

const loginHost = "https://login.microsoftonline.com"

// authorityURL builds the authorize or token URL for a tenant; an empty tenant
// is the /common/ authority every customer used before tenants existed. It is
// the only place a login URL is built. A tenant that fails ValidateTenant is
// an error and never a fallback to /common/.
func authorityURL(tenant, endpoint string) (string, error) {
	if endpoint != "authorize" && endpoint != "token" {
		return "", fmt.Errorf("unknown login endpoint %q", endpoint)
	}
	segment := "common"
	if tenant != "" {
		v, err := ValidateTenant(tenant)
		if err != nil {
			return "", fmt.Errorf("tenant for this customer: %w", err)
		}
		segment = url.PathEscape(v)
	}
	return loginHost + "/" + segment + "/oauth2/v2.0/" + endpoint, nil
}

// tenantIDFromToken reads the issuing tenant's ID out of an Entra access
// token (the "tid" claim). The signature is not checked: the token is one
// futils itself received over TLS from the token endpoint, and the ID is used
// to decide whether a cached token belongs to the customer's tenant and to
// record which tenant a bind landed in. Returns false for anything that is
// not a token carrying a GUID tid.
func tenantIDFromToken(accessToken string) (string, bool) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false
	}
	var claims struct {
		TID string `json:"tid"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false
	}
	tid := strings.ToLower(claims.TID)
	if !tenantIDRe.MatchString(tid) {
		return "", false
	}
	return tid, true
}

// Entra error codes that mean the tenant or the account is wrong, as opposed
// to a sign-in that merely has to be repeated.
const (
	entraUnknownTenant      = "AADSTS90002" // no tenant with that ID or domain
	entraAccountNotInTenant = "AADSTS50020" // account is not a member or guest
	entraAccountNotInvited  = "AADSTS90072" // external account, never added
)

var entraCodeRe = regexp.MustCompile(`AADSTS\d+`)

// entraCode returns the AADSTS code in an error from Entra, or "". It reads
// the message, so it works the same for a token reply, a browser redirect
// and any error that wraps one.
func entraCode(err error) string {
	if err == nil {
		return ""
	}
	return entraCodeRe.FindString(err.Error())
}

// explainTenantError turns the Entra replies a wrong tenant or a wrong account
// produce into a sentence that names the tenant. Any other error is returned
// unchanged.
func explainTenantError(tenant string, err error) error {
	switch entraCode(err) {
	case entraUnknownTenant:
		return fmt.Errorf("Entra does not know a tenant called %s. Check the spelling", tenant)
	case entraAccountNotInTenant, entraAccountNotInvited:
		return fmt.Errorf("the account you signed in with has no access to %s. Sign in with an account that belongs to it, or pick another tenant", tenant)
	}
	return err
}

// browserAfterRefusedGrant is what happens when the stored sign-in did not
// yield a token at a tenant (grantErr says why): the browser opens at that
// tenant. Two exceptions keep it honest. A tenant Entra has never heard of
// fails the same way in a browser, which would show an error page and then
// wait out the two-minute timeout, so that stops here with the tenant named.
// And unless there simply was no stored sign-in, the reason is printed first.
// No cause is guessed: the same line covers a tenant that wants its own
// sign-in step, an expired token and a network error. label is how the tenant
// is shown to the user.
func browserAfterRefusedGrant(tenant, label string, grantErr error) (tokenResponse, error) {
	if entraCode(grantErr) == entraUnknownTenant {
		return tokenResponse{}, explainTenantError(label, grantErr)
	}
	if !errors.Is(grantErr, ErrNoRefreshToken) {
		fmt.Fprintf(authNotice, "Could not renew the saved sign-in for this customer's tenant (%v). A browser will open. Sign in with the same account.\n", grantErr)
	}
	return browserSignIn(tenant)
}

// ErrNoRefreshToken means the keyring holds no refresh token for the profile:
// the customer has never signed in, or its tokens were cleared.
var ErrNoRefreshToken = errors.New("no stored sign-in")

// Listing tenants goes through Azure Resource Manager, the same lookup
// `az login` does. The refresh token futils already holds can be exchanged
// for a Resource Manager token without a new consent, so no extra sign-in is
// needed. That token is used for the one listing and never stored.
const (
	armScope       = "https://management.azure.com/.default offline_access"
	armHost        = "management.azure.com"
	armTenantsURL  = "https://" + armHost + "/tenants?api-version=2022-12-01"
	maxTenantPages = 10
)

// armGet is the seam for the Azure Resource Manager GET so tests can stub it.
// It uses doGetOnce and not doGet on purpose: doGet answers a 401 by fetching
// a fresh Fabric token, which is the wrong token for Resource Manager.
var armGet = func(rawURL, token string) ([]byte, error) {
	body, status, _, err := doGetOnce(token, rawURL)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from Azure Resource Manager", status)
	}
	return body, nil
}

// ListTenants returns the tenants the customer's signed-in account belongs
// to, sorted by default domain. It needs a prior sign-in for the customer
// (ErrNoRefreshToken otherwise) and leaves the cached Fabric tokens alone.
func ListTenants(customer string) ([]Tenant, error) {
	rt, err := keyring.Get(keyringService, keyFor(customer, "refresh_token"))
	if err != nil || rt == "" {
		return nil, ErrNoRefreshToken
	}
	// /common/ on purpose: the listing is about the account, not about the
	// tenant this customer may already be bound to.
	tr, err := tokenGrant("", armScope, rt)
	if err != nil {
		return nil, err
	}

	var tenants []Tenant
	next := armTenantsURL
	for page := 0; page < maxTenantPages && next != ""; page++ {
		body, err := armGet(next, tr.AccessToken)
		if err != nil {
			return nil, err
		}
		var reply struct {
			Value []struct {
				TenantID      string `json:"tenantId"`
				DisplayName   string `json:"displayName"`
				DefaultDomain string `json:"defaultDomain"`
			} `json:"value"`
			NextLink string `json:"nextLink"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, fmt.Errorf("parse tenant list: %w", err)
		}
		for _, v := range reply.Value {
			if v.TenantID == "" {
				continue
			}
			tenants = append(tenants, Tenant{
				ID:            strings.ToLower(v.TenantID),
				DisplayName:   v.DisplayName,
				DefaultDomain: strings.ToLower(v.DefaultDomain),
			})
		}
		// The bearer token only ever goes to Azure Resource Manager, whatever
		// the reply's link says.
		next = ""
		if u, err := url.Parse(reply.NextLink); err == nil && u.Scheme == "https" && u.Host == armHost {
			next = reply.NextLink
		}
	}
	sort.SliceStable(tenants, func(i, j int) bool {
		if tenants[i].DefaultDomain != tenants[j].DefaultDomain {
			return tenants[i].DefaultDomain < tenants[j].DefaultDomain
		}
		return tenants[i].ID < tenants[j].ID
	})
	return tenants, nil
}

// Binding is the result of a successful BindTenant: a Fabric token issued by
// the tenant, and that tenant's ID.
type Binding struct {
	Token    string
	TenantID string
}

// BindTenant signs the customer in to the given tenant (an ID or a domain)
// and caches the resulting tokens under the customer. It tries the stored
// sign-in first, which needs no browser when the tenant accepts it, and falls
// back to a browser sign-in at that tenant. Nothing is stored unless a token
// was obtained, so a wrong or mistyped tenant leaves the customer as it was.
//
// The returned TenantID is read from the token, so a typed domain comes back
// as the tenant's ID. Callers save that ID on the customer; this function
// does not touch the config.
func BindTenant(customer, tenant string) (Binding, error) {
	tenant, err := ValidateTenant(tenant)
	if err != nil {
		return Binding{}, err
	}

	var tr tokenResponse
	var grantErr error = ErrNoRefreshToken
	if rt, kerr := keyring.Get(keyringService, keyFor(customer, "refresh_token")); kerr == nil && rt != "" {
		tr, grantErr = tokenGrant(tenant, scope, rt)
	}
	if grantErr != nil {
		if tr, err = browserAfterRefusedGrant(tenant, tenant, grantErr); err != nil {
			return Binding{}, err
		}
	}

	id, ok := tenantIDFromToken(tr.AccessToken)
	if !ok {
		if !tenantIDRe.MatchString(tenant) {
			return Binding{}, fmt.Errorf("could not read the tenant ID for %s. Type the tenant ID instead", tenant)
		}
		// The token came from this ID's own token URL, so the ID stands.
		id = tenant
	}
	saveTokens(customer, tr)
	return Binding{Token: tr.AccessToken, TenantID: id}, nil
}
