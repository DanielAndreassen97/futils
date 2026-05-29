package cmd

import (
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

var (
	listHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(ui.AccentColor)
	listLabelStyle  = lipgloss.NewStyle().Foreground(ui.DimColor)
)

func List(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Customers) == 0 {
		fmt.Println("No customers configured. Add a customer first.")
		return nil
	}

	names := sortedCustomerNames(cfg)
	for i, name := range names {
		customer := cfg.Customers[name]
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(listHeaderStyle.Render(name))
		if len(customer.Environments) == 0 {
			fmt.Printf("  %s (none — run 'futils edit' to add one)\n", listLabelStyle.Render("Environments:"))
			continue
		}
		fmt.Println(listLabelStyle.Render("  Environments:"))
		for _, e := range customer.Environments {
			switch len(e.Workspaces) {
			case 0:
				fmt.Printf("    %-12s (no workspaces)\n", e.Alias)
			case 1:
				fmt.Printf("    %-12s → %s\n", e.Alias, e.Workspaces[0])
			default:
				fmt.Printf("    %-12s →\n", e.Alias)
				for _, ws := range e.Workspaces {
					fmt.Printf("                   • %s\n", ws)
				}
			}
		}
	}
	return nil
}
