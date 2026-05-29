// futils is an interactive CLI for Microsoft Fabric. It runs notebooks
// with parameter overrides, refreshes semantic-model tables, copies
// items between workspaces, and manages per-customer workspace configs.
// Authentication is via Entra ID (Azure CLI public client) with tokens
// cached in the OS keychain.
//
// Usage:
//
//	futils               # interactive menu
//	futils run           # run a notebook
//	futils refresh       # refresh semantic-model tables
//	futils move          # copy an item between workspaces
//	futils --version     # print version
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/DanielAndreassen97/futils/cmd"
	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// version is overridden by the release build via -ldflags.
var version = "dev"

func main() {
	ui.Version = version
	fabric.SetUserAgent(version)
	configPath := config.GetConfigPath()
	args := os.Args[1:]

	if len(args) == 0 {
		cmd.MainMenu(configPath)
		return
	}

	var err error
	switch args[0] {
	case "run":
		err = cmd.Run(configPath)
	case "refresh":
		err = cmd.Refresh(configPath)
	case "move":
		err = cmd.Move(configPath)
	case "favorites", "favourites":
		err = cmd.Favorites(configPath)
	case "add":
		err = cmd.Add(configPath)
	case "edit":
		err = cmd.Edit(configPath)
	case "remove":
		err = cmd.Remove(configPath)
	case "list":
		err = cmd.List(configPath)
	case "logout":
		err = cmd.Logout(configPath)
	case "help", "--help", "-h":
		cmd.Help()
	case "version", "--version", "-v":
		fmt.Printf("futils %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		os.Exit(1)
	}
	if err != nil {
		// ErrQuit and ErrGoBack surface when the user presses ctrl+c or
		// esc — exit quietly rather than printing a confusing error.
		if errors.Is(err, ui.ErrQuit) || errors.Is(err, ui.ErrGoBack) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
