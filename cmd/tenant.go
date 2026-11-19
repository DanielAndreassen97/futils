package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// Tenant binding, command layer. A customer can be bound to one Entra tenant
// so every sign-in for it goes there. Without a binding, sign-in lands in the
// account's own tenant, which is wrong when the account is a guest in the
// customer's tenant: the customer then lists another company's workspaces.
//
// Two ways in: the "Tenant" row in the customer edit menu (editTenant), and
// the question signInCustomer asks on its own for a customer that has no
// tenant and no environments yet.

// tenantAPI is the part of a client that can list and bind tenants. Only the
// real client implements it. Demo mode and test fakes have no sign-in behind
// them, so they don't, and every tenant screen is hidden for them. This is the
// same optional-interface pattern as oneLakeProvider in schemacompare.go.
type tenantAPI interface {
	ListTenants(customer string) ([]fabric.Tenant, error)
	BindTenant(customer, tenant string) (fabric.Binding, error)
	ClearTokens(customer string) error
}

func (RealAPIClient) ListTenants(customer string) ([]fabric.Tenant, error) {
	return fabric.ListTenants(customer)
}
func (RealAPIClient) BindTenant(customer, tenant string) (fabric.Binding, error) {
	return fabric.BindTenant(customer, tenant)
}
func (RealAPIClient) ClearTokens(customer string) error {
	return fabric.ClearCachedTokens(customer)
}

// Swap hooks for testing, as in move.go: tests replace these to drive the
// flow without spinning up bubbletea.
var (
	tenantFilterPicker           = ui.FilterMenu
	tenantNumberPicker           = ui.NumberMenu
	tenantPromptInput            = defaultPromptInput
	tenantConfirm                = ui.Confirm
	tenantOut          io.Writer = os.Stdout
)

const (
	tenantTypeIt       = "__type_tenant"
	tenantActionChange = "change"
	tenantActionClear  = "clear"
	tenantActionBack   = "back"
)

// TenantResolver returns the customer → tenant lookup main.go installs in the
// fabric package. It reads the config on every call, so a tenant bound or
// cleared in a running session takes effect at once. An unknown customer and
// a missing config file mean "no tenant"; a config that cannot be read is an
// error, because guessing "no tenant" would sign a bound customer in to the
// account's own tenant.
func TenantResolver(configPath string) func(customer string) (fabric.Tenant, error) {
	return func(customer string) (fabric.Tenant, error) {
		cfg, err := config.Load(configPath)
		if err != nil {
			return fabric.Tenant{}, fmt.Errorf("read config: %w", err)
		}
		t := cfg.Customers[customer].Tenant
		if t.ID == "" && t.Domain == "" {
			return fabric.Tenant{}, nil
		}
		// The ID is compared with the tenant inside each cached token, so it
		// must be the GUID. A domain there (or no ID at all) would sign in but
		// never match, and the cache would be skipped on every call.
		if !fabric.IsTenantID(t.ID) {
			return fabric.Tenant{}, fmt.Errorf("customer %q has a tenant in the config without a valid tenant ID. Set the tenant again under Edit customer > Tenant", customer)
		}
		return fabric.Tenant{ID: t.ID, DefaultDomain: t.Domain}, nil
	}
}

// tenantBadge is the tag on the menu row: the bound tenant's domain, the
// start of its ID when no domain is known, or DEFAULT when unbound.
func tenantBadge(c config.Customer) string {
	if c.Tenant.ID == "" {
		return "DEFAULT"
	}
	return tenantLabel(c.Tenant)
}

func tenantLabel(t config.TenantRef) string {
	return displayTenant(t.Domain, t.ID)
}

// displayTenant is how a tenant is shown: its domain when one is known, else
// the start of its ID. One rule, shared with the sign-in line.
func displayTenant(domain, id string) string {
	return fabric.Tenant{ID: id, DefaultDomain: domain}.Label()
}

// signInCustomer is GetAccessToken plus the tenant question. It is used at the
// sign-in sites a customer without environments can reach. For a customer
// that has no tenant and no environments, and an account that belongs to more
// than one tenant, it asks which tenant the customer lives in before the
// caller lists workspaces. Every other customer gets exactly GetAccessToken.
//
// The question is never skipped silently: a tenant list that cannot be
// fetched, a bind that fails, and Esc each print what happened, because the
// alternative is the wrong tenant's workspaces with no explanation.
//
// A pick saves the tenant on the customer in the config. A caller that loaded
// the customer before this call must therefore not save that older copy
// afterwards: it saves through config.UpdateCustomer instead.
func signInCustomer(configPath string, client APIClient, customerName string) (string, error) {
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return "", err
	}
	api, ok := client.(tenantAPI)
	if !ok {
		return token, nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	customer := cfg.Customers[customerName]
	if customer.Tenant.ID != "" || len(customer.Environments) > 0 {
		return token, nil
	}

	tenants, err := api.ListTenants(customerName)
	if err != nil {
		fmt.Fprintf(tenantOut, "Couldn't check which tenants this account belongs to (%v). If the workspaces below look wrong, set the tenant under Edit customer > Tenant.\n", err)
		return token, nil
	}
	if len(tenants) < 2 {
		return token, nil
	}

	fmt.Fprintln(tenantOut, infoStyle.Render("This account belongs to more than one tenant."))
	title := fmt.Sprintf("Which tenant is %s in? (esc = decide later)", customerName)
	for {
		input, domain, err := chooseTenant(title, tenants, "")
		if errors.Is(err, ui.ErrGoBack) {
			fmt.Fprintln(tenantOut, "No tenant set. The workspaces below come from your account's own tenant. Change this under Edit customer > Tenant.")
			return token, nil
		}
		if err != nil {
			return "", err
		}
		bound, err := bindAndSaveTenant(configPath, client, api, customerName, input, domain)
		if errors.Is(err, errTenantSignIn) {
			continue // the reason was printed; let the user pick again
		}
		return bound, err
	}
}

// editTenant is the action behind the "Tenant" row: bind an unbound customer,
// or change or clear an existing binding.
func editTenant(configPath string, client APIClient, customerName string) error {
	api, ok := client.(tenantAPI)
	if !ok {
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}

	if customer.Tenant.ID != "" {
		choice, err := tenantNumberPicker(fmt.Sprintf("Tenant for %s: %s", customerName, tenantBadge(customer)), []ui.MenuOption{
			{Label: "Change tenant", Value: tenantActionChange, Description: "Pick another tenant for this customer."},
			{Label: "Clear tenant", Value: tenantActionClear, Description: "Go back to the default sign-in: your account's own tenant."},
			{Label: "Back", Value: tenantActionBack},
		})
		if err != nil {
			return err
		}
		switch choice {
		case tenantActionClear:
			return clearTenant(configPath, api, customerName, customer)
		case tenantActionBack:
			return nil
		}
	}

	// The list needs only the stored sign-in, so it is tried first. Signing in
	// up front would open a browser at the tenant the user is trying to leave
	// whenever that tenant no longer accepts the account. Only a customer that
	// has never signed in gets a sign-in here.
	tenants, err := api.ListTenants(customerName)
	if errors.Is(err, fabric.ErrNoRefreshToken) {
		if _, serr := client.GetAccessToken(customerName); serr == nil {
			tenants, err = api.ListTenants(customerName)
		}
	}
	if err != nil {
		fmt.Fprintf(tenantOut, "Couldn't list your tenants (%v). Type the tenant instead.\n", err)
		tenants = nil
	}

	title := fmt.Sprintf("Which tenant is %s in?", customerName)
	for {
		input, domain, err := chooseTenant(title, tenants, customer.Tenant.ID)
		if err != nil {
			return err
		}
		if len(customer.Environments) > 0 && !sameTenant(input, customer.Tenant) {
			previous := "the default sign-in"
			if customer.Tenant.ID != "" {
				previous = tenantLabel(customer.Tenant)
			}
			target := displayTenant(domain, input)
			confirmed, err := tenantConfirm(fmt.Sprintf("Environments on %s were set up under %s. Their workspaces may not exist in %s. Use %s for this customer anyway?", customerName, previous, target, target))
			if err != nil {
				return err
			}
			if !confirmed {
				return nil
			}
		}
		_, err = bindAndSaveTenant(configPath, client, api, customerName, input, domain)
		if errors.Is(err, errTenantSignIn) {
			continue // the reason was printed; pick or type again
		}
		return err
	}
}

func sameTenant(input string, current config.TenantRef) bool {
	if current.ID == "" {
		return false
	}
	return strings.EqualFold(input, current.ID) || (current.Domain != "" && strings.EqualFold(input, current.Domain))
}

func clearTenant(configPath string, api tenantAPI, customerName string, customer config.Customer) error {
	confirmed, err := tenantConfirm(fmt.Sprintf("Clear the tenant for %s? futils will sign in to your account's own tenant for this customer again.", customerName))
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	err = config.UpdateCustomer(configPath, customerName, func(c *config.Customer) { c.Tenant = config.TenantRef{} })
	if err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	// The cached tokens were issued by the tenant just cleared. An unbound
	// customer's cache is not checked against a tenant, so they must go.
	if err := api.ClearTokens(customerName); err != nil {
		fmt.Fprintf(tenantOut, "%s\n", warningStyle.Render(fmt.Sprintf("Tenant cleared, but cached tokens may remain in the keyring: %v", err)))
		return nil
	}
	fmt.Fprintln(tenantOut, "Tenant cleared.")
	return nil
}

// chooseTenant returns the tenant the user picked or typed, and the domain to
// show for it ("" when only an ID is known). With no list (tenants == nil) it
// goes straight to typing. Esc returns ui.ErrGoBack.
func chooseTenant(title string, tenants []fabric.Tenant, currentID string) (input, domain string, err error) {
	for {
		if len(tenants) > 0 {
			options := make([]ui.FilterOption, 0, len(tenants)+1)
			for _, t := range tenants {
				parts := make([]string, 0, 3)
				if t.DefaultDomain != "" {
					parts = append(parts, t.DefaultDomain)
				}
				if t.DisplayName != "" {
					parts = append(parts, t.DisplayName)
				}
				parts = append(parts, shortGUID(t.ID))
				opt := ui.FilterOption{Label: strings.Join(parts, " · "), Value: t.ID}
				if currentID != "" && strings.EqualFold(t.ID, currentID) {
					opt.Badge = "CURRENT"
				}
				options = append(options, opt)
			}
			options = append(options, ui.FilterOption{Label: "Not listed, type it", Value: tenantTypeIt})
			choice, err := tenantFilterPicker(title, options, nil)
			if err != nil {
				return "", "", err
			}
			if choice != tenantTypeIt {
				return choice, domainFor(tenants, choice), nil
			}
		}

		typed, err := promptTenant()
		if errors.Is(err, ui.ErrGoBack) && len(tenants) > 0 {
			continue // back from the prompt to the list
		}
		if err != nil {
			return "", "", err
		}
		// Show a domain for the typed value when one is known: the value
		// itself if it is a domain, else the matching list entry's.
		if strings.Contains(typed, ".") {
			return typed, typed, nil
		}
		return typed, domainFor(tenants, typed), nil
	}
}

// domainFor is the default domain of the listed tenant with that ID, or "".
func domainFor(tenants []fabric.Tenant, id string) string {
	for _, t := range tenants {
		if t.ID == id {
			return t.DefaultDomain
		}
	}
	return ""
}

// promptTenant asks for a tenant ID or domain until the value has the shape of
// one. Whether the tenant exists is settled by the bind that follows.
func promptTenant() (string, error) {
	for {
		typed, err := tenantPromptInput("Tenant ID or domain", "contoso.no")
		if err != nil {
			return "", err
		}
		value, err := fabric.ValidateTenant(typed)
		if err != nil {
			fmt.Fprintln(tenantOut, "  That is not a tenant ID or a domain. Example: contoso.no")
			continue
		}
		return value, nil
	}
}

// errTenantSignIn marks a bind that failed at the sign-in itself. The reason
// has been shown and nothing was changed, so the caller lets the user choose
// again. Any other error from bindAndSaveTenant is a real failure to report.
var errTenantSignIn = errors.New("tenant sign-in failed")

// bindAndSaveTenant signs the customer in to the tenant, saves the tenant on
// the customer, and shows what is visible there. It returns the token for the
// new tenant.
//
// A failed sign-in prints the reason, changes nothing and returns
// errTenantSignIn. The tenant is saved before the workspace preview on
// purpose: that listing can go through the 401 retry, which looks the tenant
// up in the config and would otherwise fetch the previous tenant's workspaces.
func bindAndSaveTenant(configPath string, client APIClient, api tenantAPI, customerName, input, domain string) (string, error) {
	shown := displayTenant(domain, input)
	fmt.Fprintln(tenantOut, infoStyle.Render(fmt.Sprintf("Signing in to %s...", shown)))
	binding, err := api.BindTenant(customerName, input)
	if err != nil {
		fmt.Fprintf(tenantOut, "  %s\n", warningStyle.Render(fmt.Sprintf("Could not sign in to %s: %v", shown, err)))
		return "", errTenantSignIn
	}

	tenant := config.TenantRef{ID: binding.TenantID, Domain: domain}
	err = config.UpdateCustomer(configPath, customerName, func(c *config.Customer) { c.Tenant = tenant })
	if err != nil {
		err = fmt.Errorf("save tenant: %w", err)
		// The keyring now holds the new tenant's tokens for a customer the
		// config does not bind to it. Drop them rather than serve them.
		if cerr := api.ClearTokens(customerName); cerr != nil {
			return "", fmt.Errorf("%w (and the new sign-in could not be cleared from the keyring: %v)", err, cerr)
		}
		return "", err
	}

	// The user recognises a customer by its workspaces, not by a tenant name,
	// so the pick is confirmed with what is visible there.
	if workspaces, werr := client.ListWorkspaces(binding.Token); werr != nil {
		fmt.Fprintf(tenantOut, "  %s\n", warningStyle.Render(fmt.Sprintf("Signed in, but could not list workspaces: %v", werr)))
	} else {
		fmt.Fprintf(tenantOut, "  %s\n", workspacePreview(shown, workspaces))
	}
	fmt.Fprintf(tenantOut, "Tenant set to %s.\n", tenantLabel(tenant))
	return binding.Token, nil
}

// workspacePreview is the one-line confirmation after a bind: how many
// workspaces the account sees in the tenant, and the first few by name. The
// personal "My workspace" exists in every tenant, so it is left out of both.
func workspacePreview(tenant string, workspaces []fabric.Workspace) string {
	var names []string
	for _, w := range workspaces {
		if w.Type != "Personal" {
			names = append(names, w.DisplayName)
		}
	}
	n := len(names)
	if n == 0 {
		return fmt.Sprintf("%s: 0 workspaces visible. If that is unexpected, open Tenant again and pick another.", tenant)
	}
	if len(names) > 3 {
		names = names[:3]
	}
	return fmt.Sprintf("%s: %d workspace%s visible, e.g. %s", tenant, n, pluralS(n), strings.Join(names, ", "))
}
