package cmd

import "fmt"

// Help prints a concise usage summary. Kept as a static string rather than
// generated from flag metadata — the interactive flows don't have flags
// anyway, and a hand-written block reads better than auto-formatted help.
func Help() {
	fmt.Println(`Usage: futils [command]

Actions:
  run         Select a customer, environment, notebook, and parameters to run
  refresh     Refresh tables in a semantic model via the Enhanced Refresh API
  move        Copy a Report, Semantic Model, or Notebook between workspaces

Settings:
  favourites  Pin a customer's favourite notebooks and parameters for the run menu
  add         Add a new customer configuration
  edit        Edit an existing customer's workspace pattern or environments
  remove      Remove a customer configuration
  list        Show all configured customers
  logout      Clear cached OAuth tokens from the OS keychain

Other:
  help        Show this help message
  version     Print the current version

Run without arguments to use the interactive menu.`)
}
