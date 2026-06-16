package cmd

import (
	"fmt"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// deployItemScope is the set of types Phase 1 publishes / flags as orphans.
var deployItemScope = map[string]bool{
	"Notebook": true, "DataPipeline": true, "SemanticModel": true, "Report": true,
}

// Deploy is the top-level entry point for the `deploy` subcommand.
func Deploy(configPath string) error { return DeployWithAPI(configPath, DefaultAPI) }

// DeployWithAPI is the testable entry: pick customer -> resolve source from
// origin/main -> pick target workspace -> compare -> cherry-pick -> publish.
func DeployWithAPI(configPath string, client APIClient) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Customers) == 0 {
		fmt.Println("No customers configured. Add a customer first.")
		return nil
	}
	customerName, customer, err := selectCustomer(cfg)
	if err != nil {
		return err
	}

	repoPath := customer.RepoPath
	if repoPath == "" {
		repoPath, err = defaultPromptInput("Path to the Fabric git repo", "/Users/you/Repos/...")
		if err != nil {
			return err
		}
	}

	src, err := deploy.NewSource(repoPath)
	if err != nil {
		return err
	}
	sp := ui.NewSpinner(fmt.Sprintf("Fetching and reading %s...", src.Ref()))
	sp.Start()
	var local []deploy.LocalItem
	var fetchErr, discErr error
	func() {
		defer sp.Stop()
		if fetchErr = src.Fetch(); fetchErr != nil {
			return
		}
		local, discErr = src.DiscoverItems()
	}()
	if fetchErr != nil {
		return fetchErr
	}
	if discErr != nil {
		return discErr
	}
	if len(local) == 0 {
		return fmt.Errorf("no Fabric items found at %s", src.Ref())
	}

	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	target, err := pickWorkspace("Select target workspace", workspaces, "")
	if err != nil {
		return err
	}

	// parameter.yml is optional. Only ask for an environment key when it
	// exists and actually has rules — otherwise the key would go unused.
	var params deploy.Parameters
	hasParams := false
	if raw, perr := src.ReadFile("parameter.yml"); perr == nil {
		params, err = deploy.ParseParameters(raw)
		if err != nil {
			return err
		}
		hasParams = len(params.FindReplace) > 0 || len(params.KeyValueReplace) > 0 || len(params.SparkPool) > 0
	}

	env := ""
	if hasParams {
		env, err = promptEnvKey()
		if err != nil {
			return err
		}
	}

	results, err := runDeploy(client, token, target, env, local, params, pickDeployRows, ui.Confirm)
	if err != nil {
		return err
	}
	printDeployResults(results)
	return nil
}

// runDeploy is the non-interactive core: compare, cherry-pick (via selectRows),
// confirm, and execute. Separated so tests drive it without bubbletea.
func runDeploy(
	client deploy.FabricClient,
	token string,
	target fabric.Workspace,
	env string,
	local []deploy.LocalItem,
	params deploy.Parameters,
	selectRows func([]deploy.CompareRow) ([]deploy.CompareRow, error),
	confirm func(string) (bool, error),
) ([]deploy.Result, error) {
	deployed, err := client.ListItems(token, target.ID)
	if err != nil {
		return nil, fmt.Errorf("list target items: %w", err)
	}
	rows := deploy.Compare(local, deployed, deployItemScope)

	chosen, err := selectRows(rows)
	if err != nil {
		return nil, err
	}
	var selected []deploy.LocalItem
	orphansSkipped := 0
	for _, r := range chosen {
		if r.Class == deploy.ClassOrphan {
			// Phase 1 shows orphans for visibility but cannot delete them.
			orphansSkipped++
			continue
		}
		selected = append(selected, r.Local)
	}
	if orphansSkipped > 0 {
		fmt.Printf("Note: %d orphaned item(s) skipped — deleting orphans isn't supported yet.\n", orphansSkipped)
	}
	if len(selected) == 0 {
		fmt.Println("Nothing selected to deploy.")
		return nil, nil
	}

	ok, err := confirm(fmt.Sprintf("Deploy %d item(s) to %s?", len(selected), target.DisplayName))
	if err != nil {
		return nil, err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil, nil
	}

	plan := deploy.BuildPlan(selected, deployed)
	sp := ui.NewSpinner("Publishing...")
	sp.Start()
	results, err := deploy.Execute(client, token, target, env, plan, params)
	sp.Stop()
	return results, err
}

// pickDeployRows shows the compare result as a checkbox list, pre-checking
// New and Exists rows (orphans are shown but unchecked). Returns chosen rows.
func pickDeployRows(rows []deploy.CompareRow) ([]deploy.CompareRow, error) {
	labels := make([]string, len(rows))
	initial := make([]string, 0, len(rows))
	byLabel := make(map[string]deploy.CompareRow, len(rows))
	for i, r := range rows {
		label := fmt.Sprintf("%-7s %-14s %s", r.Class, r.ItemType(), r.Name())
		labels[i] = label
		byLabel[label] = r
		if r.Class != deploy.ClassOrphan {
			initial = append(initial, label)
		}
	}
	chosen, err := ui.MultiSelect("Select items to deploy", labels, initial)
	if err != nil {
		return nil, err
	}
	out := make([]deploy.CompareRow, 0, len(chosen))
	for _, l := range chosen {
		out = append(out, byLabel[l])
	}
	return out, nil
}

// promptEnvKey asks for the parameter.yml environment key (e.g. TEST, PROD).
func promptEnvKey() (string, error) {
	return defaultPromptInput("parameter.yml environment key (e.g. TEST, PROD)", "TEST")
}

func printDeployResults(results []deploy.Result) {
	if len(results) == 0 {
		return
	}
	var failed int
	var b string
	for _, r := range results {
		if r.Err != nil {
			failed++
			b += fmt.Sprintf("  ✗ %s (%s): %v\n", r.Name, r.Type, r.Err)
		} else {
			b += fmt.Sprintf("  ✓ %s (%s) %s\n", r.Name, r.Type, r.Action)
		}
	}
	fmt.Println()
	if failed > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf("Deploy finished with %d failure(s)\n%s", failed, b)))
	} else {
		fmt.Println(successStyle.Render(fmt.Sprintf("Deployed %d item(s)\n%s", len(results), b)))
	}
}
