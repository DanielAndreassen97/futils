// Tests for choosing and binding a customer's tenant from the menus.
//
// What it protects: the "Tenant" row in the customer edit menu, and the
// question futils asks on its own when a new customer's account belongs to
// more than one tenant.
//
// Why we need it: an account that is a guest in a customer's tenant otherwise
// lists its own tenant's workspaces with no explanation. If these flows
// regress, the user is back to seeing another company's workspaces under the
// customer they just added, or a wrong pick is saved with no way to see it.
//
// Example: adding the customer "Contoso" with an account that belongs to
// Fabrikam and Contoso shows both tenants, and picking contoso.no saves that
// tenant on the customer and returns a token for it. An account with one
// tenant is asked nothing.
package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

const (
	idContoso  = "11111111-2222-3333-4444-555555555555"
	idFabrikam = "99999999-8888-7777-6666-555555555555"
)

var twoTenants = []fabric.Tenant{
	{ID: idContoso, DisplayName: "Contoso", DefaultDomain: "contoso.no"},
	{ID: idFabrikam, DisplayName: "", DefaultDomain: "fabrikam.onmicrosoft.com"},
}

// fakeTenantClient is a client with a real sign-in behind it as far as the
// menus can tell: it lists tenants and binds them. The embedded fake supplies
// the rest of APIClient.
type fakeTenantClient struct {
	fakeMoveAPI
	tenants    []fabric.Tenant
	listErr    error
	listCalls  int
	bindErr    map[string]error // keyed by the tenant value passed to BindTenant
	bindCalls  []string
	cleared    []string
	workspaces map[string][]fabric.Workspace // keyed by token

	listErrOnce      error  // returned by the first ListTenants call only
	tokenCalls       int    // GetAccessToken calls
	onBind           func() // runs inside BindTenant, after it "succeeded"
	onListWorkspaces func() // runs inside ListWorkspaces
}

func (f *fakeTenantClient) GetAccessToken(string) (string, error) {
	f.tokenCalls++
	return f.token, nil
}

func (f *fakeTenantClient) ListTenants(string) ([]fabric.Tenant, error) {
	f.listCalls++
	if err := f.listErrOnce; err != nil {
		f.listErrOnce = nil
		return nil, err
	}
	return f.tenants, f.listErr
}

func (f *fakeTenantClient) BindTenant(_, tenant string) (fabric.Binding, error) {
	f.bindCalls = append(f.bindCalls, tenant)
	if err := f.bindErr[tenant]; err != nil {
		return fabric.Binding{}, err
	}
	id := tenant
	for _, t := range f.tenants {
		if t.DefaultDomain == tenant {
			id = t.ID
		}
	}
	if f.onBind != nil {
		f.onBind()
	}
	return fabric.Binding{Token: "token-" + id, TenantID: id}, nil
}

func (f *fakeTenantClient) ClearTokens(customer string) error {
	f.cleared = append(f.cleared, customer)
	return nil
}

func (f *fakeTenantClient) ListWorkspaces(token string) ([]fabric.Workspace, error) {
	if f.onListWorkspaces != nil {
		f.onListWorkspaces()
	}
	return f.workspaces[token], nil
}

func newTenantClient() *fakeTenantClient {
	return &fakeTenantClient{
		fakeMoveAPI: fakeMoveAPI{token: "home-token"},
		tenants:     twoTenants,
		workspaces: map[string][]fabric.Workspace{
			"token-" + idContoso: {
				{DisplayName: "My workspace", Type: "Personal"},
				{DisplayName: "DW - DEV - Config"},
				{DisplayName: "DW - DEV - SemMod"},
			},
		},
	}
}

func tenantConfig(t *testing.T, customers map[string]config.Customer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.Config{Customers: customers}); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadCustomer(t *testing.T, path, name string) config.Customer {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Customers[name]
}

const esc = "<esc>"

// tenantScript drives the tenant screens with scripted answers and records
// what each screen was shown.
type tenantScript struct {
	picks    []string // filter-picker answers; esc = go back
	menu     []string // number-menu answers
	typed    []string // text-prompt answers; esc = go back
	confirms []bool

	pickerTitles  []string
	pickerOptions [][]ui.FilterOption
	confirmAsked  []string
	prompts       int
	out           strings.Builder
}

func scriptTenantUI(t *testing.T, s *tenantScript) {
	t.Helper()
	origPick, origMenu, origPrompt, origConfirm, origOut := tenantFilterPicker, tenantNumberPicker, tenantPromptInput, tenantConfirm, tenantOut
	t.Cleanup(func() {
		tenantFilterPicker, tenantNumberPicker, tenantPromptInput, tenantConfirm, tenantOut = origPick, origMenu, origPrompt, origConfirm, origOut
	})
	next := func(queue *[]string, what string) (string, error) {
		if len(*queue) == 0 {
			t.Fatalf("%s shown more often than the test scripted", what)
		}
		v := (*queue)[0]
		*queue = (*queue)[1:]
		if v == esc {
			return "", ui.ErrGoBack
		}
		return v, nil
	}
	tenantFilterPicker = func(title string, options []ui.FilterOption, _ ui.FilterRowRenderer) (string, error) {
		s.pickerTitles = append(s.pickerTitles, title)
		s.pickerOptions = append(s.pickerOptions, options)
		return next(&s.picks, "tenant picker")
	}
	tenantNumberPicker = func(string, []ui.MenuOption) (string, error) {
		return next(&s.menu, "tenant menu")
	}
	tenantPromptInput = func(string, string) (string, error) {
		s.prompts++
		return next(&s.typed, "tenant prompt")
	}
	tenantConfirm = func(msg string) (bool, error) {
		s.confirmAsked = append(s.confirmAsked, msg)
		if len(s.confirms) == 0 {
			t.Fatalf("unexpected yes/no question: %s", msg)
		}
		v := s.confirms[0]
		s.confirms = s.confirms[1:]
		return v, nil
	}
	tenantOut = &s.out
}

func TestSignInCustomer_AsksNewCustomerWithTwoTenants(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	s := &tenantScript{picks: []string{idContoso}}
	scriptTenantUI(t, s)

	token, err := signInCustomer(path, client, "Contoso")
	if err != nil {
		t.Fatalf("signInCustomer: %v", err)
	}
	// The workspace list that follows must come from the picked tenant.
	if token != "token-"+idContoso {
		t.Errorf("token = %q, want the one from the picked tenant", token)
	}
	want := config.TenantRef{ID: idContoso, Domain: "contoso.no"}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != want {
		t.Errorf("saved tenant = %+v, want %+v", got, want)
	}
	// What the user recognises is the workspaces, so the pick is confirmed by them.
	// "My workspace" exists in every tenant, so it proves nothing and is not counted.
	if out := s.out.String(); !strings.Contains(out, "contoso.no: 2 workspaces visible") || !strings.Contains(out, "DW - DEV - Config") || strings.Contains(out, "My workspace") {
		t.Errorf("no workspace preview after the bind:\n%s", out)
	}
}

func TestTenantPickerRows(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	s := &tenantScript{picks: []string{esc}}
	scriptTenantUI(t, s)
	if _, err := signInCustomer(path, newTenantClient(), "Contoso"); err != nil {
		t.Fatal(err)
	}
	if len(s.pickerOptions) != 1 {
		t.Fatalf("picker shown %d times", len(s.pickerOptions))
	}
	rows := s.pickerOptions[0]
	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want two tenants and the typed-entry row", rows)
	}
	if rows[0].Label != "contoso.no · Contoso · 11111111…" || rows[0].Value != idContoso {
		t.Errorf("row 0 = %+v", rows[0])
	}
	// An empty display name is left out instead of leaving a gap.
	if rows[1].Label != "fabrikam.onmicrosoft.com · 99999999…" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	if rows[2].Value != tenantTypeIt {
		t.Errorf("last row must offer typing the tenant, got %+v", rows[2])
	}
}

func TestSignInCustomer_SilentWithOneTenant(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Fabrikam": {}})
	client := newTenantClient()
	client.tenants = twoTenants[1:]
	s := &tenantScript{}
	scriptTenantUI(t, s)

	token, err := signInCustomer(path, client, "Fabrikam")
	if err != nil || token != "home-token" {
		t.Fatalf("signInCustomer = %q, %v", token, err)
	}
	if s.out.Len() != 0 || len(s.pickerTitles) != 0 {
		t.Errorf("a single-tenant account was shown something: %q", s.out.String())
	}
	if got := loadCustomer(t, path, "Fabrikam").Tenant; got != (config.TenantRef{}) {
		t.Errorf("a tenant was saved without being picked: %+v", got)
	}
}

// A failed list must not look like "one tenant": that is the silent
// wrong-workspaces failure this feature exists to remove.
func TestSignInCustomer_SaysSoWhenListFails(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.listErr = errors.New("HTTP 403 from Azure Resource Manager")
	s := &tenantScript{}
	scriptTenantUI(t, s)

	token, err := signInCustomer(path, client, "Contoso")
	if err != nil || token != "home-token" {
		t.Fatalf("signInCustomer = %q, %v", token, err)
	}
	out := s.out.String()
	if !strings.Contains(out, "Couldn't check which tenants this account belongs to") || !strings.Contains(out, "HTTP 403") || !strings.Contains(out, "Edit customer > Tenant") {
		t.Errorf("list failure not explained:\n%s", out)
	}
}

func TestSignInCustomer_DoesNotAskSettledCustomers(t *testing.T) {
	settled := map[string]config.Customer{
		"HasEnvironment": {Environments: []config.Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}}},
		"Bound":          {Tenant: config.TenantRef{ID: idContoso, Domain: "contoso.no"}},
	}
	path := tenantConfig(t, settled)
	for name := range settled {
		client := newTenantClient()
		s := &tenantScript{}
		scriptTenantUI(t, s)
		token, err := signInCustomer(path, client, name)
		if err != nil || token != "home-token" {
			t.Fatalf("%s: signInCustomer = %q, %v", name, token, err)
		}
		if client.listCalls != 0 || s.out.Len() != 0 {
			t.Errorf("%s: the tenant list was fetched (%d calls) or something was printed (%q)", name, client.listCalls, s.out.String())
		}
	}
}

// Demo mode and the existing test fakes have no tenant functions, so they
// must pass straight through.
func TestSignInCustomer_ClientWithoutTenantFunctions(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	s := &tenantScript{}
	scriptTenantUI(t, s)
	token, err := signInCustomer(path, &fakeMoveAPI{token: "home-token"}, "Contoso")
	if err != nil || token != "home-token" {
		t.Fatalf("signInCustomer = %q, %v", token, err)
	}
	if s.out.Len() != 0 {
		t.Errorf("printed %q", s.out.String())
	}
}

func TestSignInCustomer_EscLeavesUnboundAndSaysSo(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	s := &tenantScript{picks: []string{esc}}
	scriptTenantUI(t, s)

	token, err := signInCustomer(path, client, "Contoso")
	if err != nil || token != "home-token" {
		t.Fatalf("signInCustomer = %q, %v", token, err)
	}
	if len(client.bindCalls) != 0 || loadCustomer(t, path, "Contoso").Tenant != (config.TenantRef{}) {
		t.Error("Esc bound a tenant")
	}
	if !strings.Contains(s.out.String(), "No tenant set.") {
		t.Errorf("Esc was silent:\n%s", s.out.String())
	}
}

// After a failed bind the flow must not carry on with the other tenant's
// token: the user picked a tenant and would get the wrong workspaces.
func TestSignInCustomer_FailedBindShowsPickerAgain(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.bindErr = map[string]error{idFabrikam: errors.New("the account you signed in with has no access to fabrikam.onmicrosoft.com")}
	s := &tenantScript{picks: []string{idFabrikam, idContoso}}
	scriptTenantUI(t, s)

	token, err := signInCustomer(path, client, "Contoso")
	if err != nil || token != "token-"+idContoso {
		t.Fatalf("signInCustomer = %q, %v", token, err)
	}
	if len(s.pickerTitles) != 2 {
		t.Errorf("picker shown %d times, want 2", len(s.pickerTitles))
	}
	if !strings.Contains(s.out.String(), "has no access to fabrikam.onmicrosoft.com") {
		t.Errorf("the bind failure was not shown:\n%s", s.out.String())
	}
	if got := loadCustomer(t, path, "Contoso").Tenant.ID; got != idContoso {
		t.Errorf("saved tenant = %q", got)
	}
}

func TestEditTenant_BindFromPicker(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	s := &tenantScript{picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	want := config.TenantRef{ID: idContoso, Domain: "contoso.no"}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != want {
		t.Errorf("saved tenant = %+v, want %+v", got, want)
	}
	if !strings.Contains(s.out.String(), "Tenant set to contoso.no") {
		t.Errorf("no confirmation:\n%s", s.out.String())
	}
}

func TestEditTenant_TypedWhenListFails(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.listErr = errors.New("HTTP 403 from Azure Resource Manager")
	// "common" is a login authority name, not a tenant: it must be refused
	// and asked for again.
	s := &tenantScript{typed: []string{"common", "contoso.no"}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	if s.prompts != 2 {
		t.Errorf("prompt shown %d times, want 2", s.prompts)
	}
	out := s.out.String()
	if !strings.Contains(out, "Couldn't list your tenants") || !strings.Contains(out, "not a tenant ID or a domain") {
		t.Errorf("missing explanation:\n%s", out)
	}
	// A typed domain is saved as the tenant's ID, with the domain for display.
	want := config.TenantRef{ID: idContoso, Domain: "contoso.no"}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != want {
		t.Errorf("saved tenant = %+v, want %+v", got, want)
	}
	if len(client.bindCalls) != 1 || client.bindCalls[0] != "contoso.no" {
		t.Errorf("bind calls = %v", client.bindCalls)
	}
}

func TestEditTenant_TypedFromPickerRow(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	s := &tenantScript{picks: []string{tenantTypeIt}, typed: []string{idContoso}}
	scriptTenantUI(t, s)
	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	// A typed ID that is in the list picks up that tenant's domain.
	want := config.TenantRef{ID: idContoso, Domain: "contoso.no"}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != want {
		t.Errorf("saved tenant = %+v, want %+v", got, want)
	}
}

func TestEditTenant_FailedBindSavesNothing(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.bindErr = map[string]error{idFabrikam: errors.New("Entra does not know a tenant called x")}
	s := &tenantScript{picks: []string{idFabrikam, esc}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil && !errors.Is(err, ui.ErrGoBack) {
		t.Fatalf("editTenant: %v", err)
	}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != (config.TenantRef{}) {
		t.Errorf("a failed bind saved %+v", got)
	}
}

func TestEditTenant_AsksBeforeRebindingCustomerWithEnvironments(t *testing.T) {
	withEnv := config.Customer{Environments: []config.Environment{{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}}}}

	path := tenantConfig(t, map[string]config.Customer{"Contoso": withEnv})
	client := newTenantClient()
	s := &tenantScript{picks: []string{idContoso}, confirms: []bool{false}}
	scriptTenantUI(t, s)
	if err := editTenant(path, client, "Contoso"); err != nil && !errors.Is(err, ui.ErrGoBack) {
		t.Fatalf("editTenant: %v", err)
	}
	if len(client.bindCalls) != 0 || loadCustomer(t, path, "Contoso").Tenant.ID != "" {
		t.Error("the tenant was changed although the user answered no")
	}
	if len(s.confirmAsked) != 1 || !strings.Contains(s.confirmAsked[0], "the default sign-in") {
		t.Errorf("question = %v", s.confirmAsked)
	}

	path = tenantConfig(t, map[string]config.Customer{"Contoso": withEnv})
	client = newTenantClient()
	s = &tenantScript{picks: []string{idContoso}, confirms: []bool{true}}
	scriptTenantUI(t, s)
	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	got := loadCustomer(t, path, "Contoso")
	if got.Tenant.ID != idContoso || len(got.Environments) != 1 {
		t.Errorf("after yes: %+v", got)
	}
}

func TestEditTenant_Clear(t *testing.T) {
	bound := config.Customer{Tenant: config.TenantRef{ID: idContoso, Domain: "contoso.no"}}
	path := tenantConfig(t, map[string]config.Customer{"Contoso": bound})
	client := newTenantClient()
	s := &tenantScript{menu: []string{tenantActionClear}, confirms: []bool{true}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	if got := loadCustomer(t, path, "Contoso").Tenant; got != (config.TenantRef{}) {
		t.Errorf("tenant after clear = %+v", got)
	}
	// Tokens from the cleared tenant must not be served to the now-unbound customer.
	if len(client.cleared) != 1 || client.cleared[0] != "Contoso" {
		t.Errorf("cached tokens cleared for %v", client.cleared)
	}
}

func TestEditTenant_ChangeMarksCurrentTenant(t *testing.T) {
	bound := config.Customer{Tenant: config.TenantRef{ID: idFabrikam, Domain: "fabrikam.onmicrosoft.com"}}
	path := tenantConfig(t, map[string]config.Customer{"Contoso": bound})
	client := newTenantClient()
	s := &tenantScript{menu: []string{tenantActionChange}, picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	rows := s.pickerOptions[0]
	if rows[0].Badge != "" || rows[1].Badge != "CURRENT" {
		t.Errorf("current tenant not marked: %+v", rows)
	}
	if got := loadCustomer(t, path, "Contoso").Tenant.ID; got != idContoso {
		t.Errorf("saved tenant = %q", got)
	}
}

func TestTenantBadge(t *testing.T) {
	cases := map[string]config.Customer{
		"contoso.no": {Tenant: config.TenantRef{ID: idContoso, Domain: "contoso.no"}},
		"11111111…":  {Tenant: config.TenantRef{ID: idContoso}},
		"DEFAULT":    {},
	}
	for want, c := range cases {
		if got := tenantBadge(c); got != want {
			t.Errorf("tenantBadge(%+v) = %q, want %q", c.Tenant, got, want)
		}
	}
}

func TestEditCustomerOptions_TenantRowNeedsTenantFunctions(t *testing.T) {
	has := func(options []ui.MenuOption) bool {
		for _, o := range options {
			if o.Value == editActionTenant {
				return true
			}
		}
		return false
	}
	bound := config.Customer{Tenant: config.TenantRef{ID: idContoso, Domain: "contoso.no"}}
	if !has(editCustomerOptions(bound, true)) {
		t.Error("the tenant row is missing for a client that can bind tenants")
	}
	if has(editCustomerOptions(bound, false)) {
		t.Error("the tenant row is shown for a client that cannot bind tenants (demo mode)")
	}
	for _, o := range editCustomerOptions(bound, true) {
		if o.Value == editActionTenant && o.Badge != "contoso.no" {
			t.Errorf("tenant row badge = %q", o.Badge)
		}
	}
}

func TestTenantResolver(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{
		"Contoso":    {Tenant: config.TenantRef{ID: idContoso, Domain: "contoso.no"}},
		"Fabrikam":   {},
		"Broken":     {Tenant: config.TenantRef{Domain: "contoso.no"}},
		"DomainAsID": {Tenant: config.TenantRef{ID: "contoso.no"}},
	})
	resolve := TenantResolver(path)

	if got, err := resolve("Contoso"); err != nil || got != (fabric.Tenant{ID: idContoso, DefaultDomain: "contoso.no"}) {
		t.Errorf("bound customer = %+v, %v", got, err)
	}
	for _, name := range []string{"Fabrikam", "NoSuchCustomer"} {
		if got, err := resolve(name); err != nil || got != (fabric.Tenant{}) {
			t.Errorf("%s = %+v, %v; want no tenant", name, got, err)
		}
	}
	// A domain with no ID cannot be compared with a token, so it is an error
	// and not a silent fallback to the default sign-in.
	if _, err := resolve("Broken"); err == nil || !strings.Contains(err.Error(), "Broken") {
		t.Errorf("tenant without an ID: err = %v", err)
	}
	// A domain where the ID belongs would sign in fine but never match a
	// token, so the cache would be skipped on every call without a word.
	if _, err := resolve("DomainAsID"); err == nil || !strings.Contains(err.Error(), "DomainAsID") {
		t.Errorf("tenant ID that is a domain: err = %v", err)
	}

	missing := TenantResolver(filepath.Join(t.TempDir(), "none.json"))
	if got, err := missing("Contoso"); err != nil || got != (fabric.Tenant{}) {
		t.Errorf("missing config = %+v, %v; want no tenant", got, err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve("Contoso"); err == nil {
		t.Error("an unreadable config must be an error, not \"no tenant\"")
	}
}

// signInCustomer can save a tenant on the customer. A flow that loaded the
// customer before signing in must not write that older copy back afterwards
// (Add environment used to, and erased the tenant the user had just picked).
// Saving through config.UpdateCustomer, as Add environment now does, keeps it.
func TestTenantPickedAtSignInSurvivesTheSaveThatFollows(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	s := &tenantScript{picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if _, err := signInCustomer(path, client, "Contoso"); err != nil {
		t.Fatalf("signInCustomer: %v", err)
	}
	err := config.UpdateCustomer(path, "Contoso", func(c *config.Customer) {
		c.Environments = append(c.Environments, config.Environment{Alias: "DEV", Workspaces: []string{"DW - DEV - Config"}})
	})
	if err != nil {
		t.Fatal(err)
	}

	got := loadCustomer(t, path, "Contoso")
	if got.Tenant.ID != idContoso || len(got.Environments) != 1 {
		t.Errorf("after saving the environment: tenant %+v, %d environment(s); the tenant must survive", got.Tenant, len(got.Environments))
	}
}

// The list needs only the stored sign-in. Signing in first would open a
// browser at the tenant the user is trying to leave when that tenant no
// longer accepts the account.
func TestEditTenant_ListsWithoutSigningInFirst(t *testing.T) {
	bound := config.Customer{Tenant: config.TenantRef{ID: idFabrikam, Domain: "fabrikam.onmicrosoft.com"}}
	path := tenantConfig(t, map[string]config.Customer{"Contoso": bound})
	client := newTenantClient()
	s := &tenantScript{menu: []string{tenantActionChange}, picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	if client.tokenCalls != 0 {
		t.Errorf("signed in %d time(s) before listing tenants, want 0", client.tokenCalls)
	}
}

func TestEditTenant_SignsInWhenNothingIsStored(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.listErrOnce = fabric.ErrNoRefreshToken
	s := &tenantScript{picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	if client.tokenCalls != 1 || client.listCalls != 2 {
		t.Errorf("sign-ins = %d, list calls = %d; want one sign-in and a second list", client.tokenCalls, client.listCalls)
	}
	if len(s.pickerTitles) != 1 {
		t.Errorf("picker shown %d times, want 1", len(s.pickerTitles))
	}
}

// If the tenant cannot be saved after a successful sign-in, the user must be
// told, and the tokens from that tenant must not stay behind for a customer
// that is still unbound in the config.
func TestSaveFailureAfterSignInIsReported(t *testing.T) {
	path := tenantConfig(t, map[string]config.Customer{"Contoso": {}})
	client := newTenantClient()
	client.onBind = func() {
		if err := config.RemoveCustomer(path, "Contoso"); err != nil {
			t.Fatal(err)
		}
	}
	s := &tenantScript{picks: []string{idContoso}}
	scriptTenantUI(t, s)

	_, err := signInCustomer(path, client, "Contoso")
	if err == nil || !strings.Contains(err.Error(), "save tenant") {
		t.Fatalf("err = %v, want the save failure", err)
	}
	if len(client.cleared) != 1 {
		t.Errorf("tokens from the unsaved tenant were left in the keyring (cleared: %v)", client.cleared)
	}
}

// The workspace preview runs through the 401 retry, which looks the tenant up
// in the config. So the new tenant must be saved before the preview, or a
// retry would fetch the previous tenant's workspaces under the new name.
func TestTenantIsSavedBeforeTheWorkspacePreview(t *testing.T) {
	bound := config.Customer{Tenant: config.TenantRef{ID: idFabrikam, Domain: "fabrikam.onmicrosoft.com"}}
	path := tenantConfig(t, map[string]config.Customer{"Contoso": bound})
	client := newTenantClient()
	var savedAtPreview string
	client.onListWorkspaces = func() { savedAtPreview = loadCustomer(t, path, "Contoso").Tenant.ID }
	s := &tenantScript{menu: []string{tenantActionChange}, picks: []string{idContoso}}
	scriptTenantUI(t, s)

	if err := editTenant(path, client, "Contoso"); err != nil {
		t.Fatalf("editTenant: %v", err)
	}
	if savedAtPreview != idContoso {
		t.Errorf("config held tenant %q when workspaces were listed, want the new one", savedAtPreview)
	}
}

func TestWorkspacePreview_OnlyPersonalWorkspaceCountsAsNothing(t *testing.T) {
	got := workspacePreview("contoso.no", []fabric.Workspace{{DisplayName: "My workspace", Type: "Personal"}})
	if !strings.Contains(got, "0 workspaces visible") || !strings.Contains(got, "pick another") {
		t.Errorf("preview = %q, want the nothing-here hint", got)
	}
}

// `futils add` says "Let's add environments" and the user presses Enter. That
// must still land on an environment row, not on the tenant screen.
func TestEditCustomerOptions_TenantRowComesAfterEnvironments(t *testing.T) {
	options := editCustomerOptions(config.Customer{}, true)
	index := func(value string) int {
		for i, o := range options {
			if o.Value == value {
				return i
			}
		}
		return -1
	}
	firstSelectable := ""
	for _, o := range options {
		if !o.IsHeader {
			firstSelectable = o.Value
			break
		}
	}
	if firstSelectable != editActionAddEnv {
		t.Errorf("first selectable row = %q, want Add environment", firstSelectable)
	}
	if index(editActionTenant) < index(editActionCommonWS) {
		t.Error("the tenant row must come after the environment rows")
	}
}
