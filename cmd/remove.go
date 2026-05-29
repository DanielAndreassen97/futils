package cmd

import (
	"fmt"
	"os"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

func Remove(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Customers) == 0 {
		fmt.Println("No customers configured.")
		return nil
	}

	selected, err := ui.NumberMenu("Select customer to remove", ui.MenuOptionsFromStrings(sortedCustomerNames(cfg)))
	if err != nil {
		return err
	}

	ok, err := ui.Confirm(fmt.Sprintf("Remove '%s'?", selected))
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil
	}

	if err := removeCustomerAndTokens(configPath, selected); err != nil {
		return err
	}
	fmt.Printf("Customer '%s' removed.\n", selected)
	return nil
}

// removeCustomerAndTokens deletes the customer from config and wipes its
// cached OAuth tokens from the keyring. Without the token wipe, a removed
// customer's long-lived refresh token is orphaned in the keyring and can no
// longer be reached by logout (which only iterates the current config), so it
// would persist indefinitely.
//
// Token cleanup failure is a warning, not a hard error: the customer is
// already gone from config (the irreversible part succeeded), and surfacing
// "tokens may remain" is more useful than failing the whole command after
// the fact.
func removeCustomerAndTokens(configPath, name string) error {
	if err := config.RemoveCustomer(configPath, name); err != nil {
		return err
	}
	if err := fabric.ClearCachedTokens(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: customer removed, but cached tokens may remain in the keyring: %v\n", err)
	}
	return nil
}
