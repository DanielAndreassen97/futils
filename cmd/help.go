package cmd

import "fmt"

// Help prints a concise usage summary. Kept as a static string rather than
// generated from flag metadata — the interactive flows don't have flags
// anyway, and a hand-written block reads better than auto-formatted help.
func Help() {
	fmt.Println(`Usage: futils [command]

Actions:
  run            Select a customer, environment, notebook, and parameters to run
  runpipeline    Select a customer, environment, and data pipeline to run
  refresh        Refresh tables in a semantic model via the Enhanced Refresh API
  move           Copy a Report, Semantic Model, or Notebook between workspaces
  deploy         Deploy a Fabric git repo to target workspaces (compare first)
  schemacompare  Compare lakehouse table schemas between two workspaces

Settings:
  favourites     Pin a customer's favourite notebooks and parameters for the run menu
  add            Add a new customer configuration
  edit           Edit a customer's environments, deploy setup, and favourites
  remove         Remove a customer configuration
  list           Show all configured customers
  logout         Clear cached OAuth tokens from the OS keychain

Other:
  demo           Explore futils against a fake offline tenant — no login, nothing sticks
  demoseed       Seed the demo sandbox only (for scripting: FUTILS_DEMO=1 FUTILS_CONFIG=…)
  help           Show this help message
  version        Print the current version

Run without arguments to use the interactive menu.`)
}
