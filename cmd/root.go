package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/DanielAndreassen97/futils/internal/ui"
)

// MainMenu is the interactive loop shown when futils is invoked with no
// arguments. Top-level entries are split into "Actions" (what you do *to*
// Fabric) and "Settings" (managing the tool's local state). Customer CRUD
// lives behind a "Manage customers" submenu so it doesn't crowd out the
// day-to-day actions in the top menu.
func MainMenu(configPath string) {
	fmt.Println(ui.Banner())
	fmt.Println()

	for {
		options := []ui.MenuOption{
			{Label: "Actions", IsHeader: true},
			{Label: "Run notebook", Value: "run"},
			{Label: "Run pipeline", Value: "runpipeline"},
			{Label: "Refresh tables", Value: "refresh"},
			{Label: "Move item", Value: "move"},
			{Label: "Deploy", Value: "deploy"},
			{Label: "Schema compare", Value: "schemacompare"},

			{Label: "Settings", IsHeader: true},
			{Label: "Manage customers", Value: "customers"},
			{Label: "Clear cached credentials", Value: "logout"},

			{Label: "Quit", Value: "quit"},
		}

		choice, err := ui.NumberMenu("What would you like to do?", options)
		if err != nil {
			// esc/b (and m — we ARE the main menu) at the top level just
			// re-shows the menu — there's nowhere to go "back" to.
			if errors.Is(err, ui.ErrGoBack) || errors.Is(err, ui.ErrGoHome) {
				continue
			}
			if errors.Is(err, ui.ErrQuit) {
				return
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}

		var cmdErr error
		switch choice {
		case "run":
			cmdErr = Run(configPath)
		case "runpipeline":
			cmdErr = RunPipelineCmd(configPath)
		case "refresh":
			cmdErr = Refresh(configPath)
		case "move":
			cmdErr = Move(configPath)
		case "deploy":
			cmdErr = Deploy(configPath)
		case "schemacompare":
			cmdErr = SchemaCompare(configPath)
		case "customers":
			cmdErr = customersSubmenu(configPath)
		case "logout":
			cmdErr = Logout(configPath)
		case "quit":
			return
		}

		if cmdErr != nil {
			// ErrGoHome lands here after unwinding whatever flow the user
			// abandoned with m — the main menu simply shows again.
			if errors.Is(cmdErr, ui.ErrGoBack) || errors.Is(cmdErr, ui.ErrGoHome) {
				continue
			}
			if errors.Is(cmdErr, ui.ErrQuit) {
				return
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", cmdErr)
		}
		fmt.Println()
	}
}

// customersSubmenu surfaces add/edit/remove/list under a single top-level
// "Manage customers" entry. Esc returns to the main menu — matches the
// "esc means back" convention used throughout the TUI.
func customersSubmenu(configPath string) error {
	for {
		options := []ui.MenuOption{
			{Label: "Add customer", Value: "add"},
			{Label: "Edit customer", Value: "edit"},
			{Label: "Remove customer", Value: "remove"},
			{Label: "List customers", Value: "list"},
			{Label: "Back", Value: "back"},
		}
		choice, err := ui.NumberMenu("Manage customers", options)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}
		var cmdErr error
		switch choice {
		case "add":
			cmdErr = Add(configPath)
		case "edit":
			cmdErr = Edit(configPath)
		case "remove":
			cmdErr = Remove(configPath)
		case "list":
			cmdErr = List(configPath)
		case "back":
			return nil
		}
		if cmdErr != nil {
			if errors.Is(cmdErr, ui.ErrGoBack) {
				continue
			}
			// Unwind both sentinels to the main loop: quit exits the app,
			// go-home lands on the main menu.
			if errors.Is(cmdErr, ui.ErrQuit) || errors.Is(cmdErr, ui.ErrGoHome) {
				return cmdErr
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", cmdErr)
		}
		fmt.Println()
	}
}
