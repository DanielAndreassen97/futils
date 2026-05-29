package cmd

import (
	"errors"
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/huh"
)

// Add prompts for a customer name, saves an empty customer, then drops
// straight into the edit sub-menu so the user can add environments
// without running a second command.
func Add(configPath string) error {
	return AddWithAPI(configPath, DefaultAPI)
}

func AddWithAPI(configPath string, client APIClient) error {
	var name string

	if err := runFormStep(huh.NewInput().Title("Customer name").Value(&name)); err != nil {
		return err
	}
	if name == "" {
		return fmt.Errorf("customer name is required")
	}

	if err := config.AddCustomer(configPath, name, config.Customer{}); err != nil {
		return err
	}
	fmt.Printf("Customer '%s' added. Let's add environments.\n", name)

	if err := editCustomerLoop(configPath, client, name); err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			return nil
		}
		return err
	}
	return nil
}
