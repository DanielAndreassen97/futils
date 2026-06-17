package cmd

import (
	"fmt"
	"os"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// deployItemScope is the set of types Phase 1 publishes / flags as orphans.
var deployItemScope = map[string]bool{
	"Notebook": true, "DataPipeline": true, "SemanticModel": true, "Report": true,
}

// deployGroup is one folder→workspace mapping resolved for a run: the items
// discovered under that folder, the target workspace, the compare rows, and the
// deployed item list (needed by BuildPlan).
type deployGroup struct {
	Folder   string
	Target   fabric.Workspace
	Rows     []deploy.CompareRow
	Deployed []fabric.Item
}

// Deploy is the top-level entry point for the `deploy` subcommand.
func Deploy(configPath string) error { return DeployWithAPI(configPath, DefaultAPI) }

// DeployWithAPI: pick customer → resolve source from origin/main → pick
// environment → resolve its folder→workspace mappings → compare per group →
// dry-run or cherry-pick+publish.
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
	if len(customer.Environments) == 0 {
		return fmt.Errorf("customer %q has no environments — add one via Edit customer first", customerName)
	}

	repoPath := customer.RepoPath
	if repoPath == "" {
		startDir, _ := os.UserHomeDir()
		repoPath, err = ui.PickDirectory("Select the Fabric git repo (enter to choose the highlighted folder)", startDir)
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
	var all []deploy.LocalItem
	var fetchErr, discErr error
	func() {
		defer sp.Stop()
		if fetchErr = src.Fetch(); fetchErr != nil {
			return
		}
		all, discErr = src.DiscoverItems()
	}()
	if fetchErr != nil {
		return fetchErr
	}
	if discErr != nil {
		return discErr
	}
	if len(all) == 0 {
		return fmt.Errorf("no Fabric items found at %s", src.Ref())
	}

	alias, err := pickEnvironment(customer)
	if err != nil {
		return err
	}

	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	groups, err := buildDeployGroups(client, token, customer, alias, all, workspaces)
	if err != nil {
		return err
	}

	// parameter.yml is optional; only ask for an env key when it has rules.
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

	dryRun, err := pickDeployMode()
	if err != nil {
		return err
	}

	results, err := runDeploy(client, token, env, groups, params, dryRun, pickGroupedRows, ui.Confirm)
	printDeployResults(results)
	if err != nil {
		return err
	}
	return nil
}

// buildDeployGroups resolves the chosen environment's folder→workspace mappings
// into compare groups. If the env has no mappings, it falls back to the Phase-1
// behavior: pick one target workspace and compare the whole repo against it.
func buildDeployGroups(client APIClient, token string, customer config.Customer, alias string, all []deploy.LocalItem, workspaces []fabric.Workspace) ([]deployGroup, error) {
	mappings, _ := customer.DeployMappings(alias)

	if len(mappings) == 0 {
		fmt.Println(infoStyle.Render(fmt.Sprintf("Env %q has no folder→workspace mappings — deploying the whole repo to one workspace.", alias)))
		target, err := pickWorkspace("Select target workspace", workspaces, "")
		if err != nil {
			return nil, err
		}
		deployed, err := client.ListItems(token, target.ID)
		if err != nil {
			return nil, fmt.Errorf("list items in %s: %w", target.DisplayName, err)
		}
		return []deployGroup{{
			Target:   target,
			Rows:     deploy.Compare(all, deployed, deployItemScope),
			Deployed: deployed,
		}}, nil
	}

	groups := make([]deployGroup, 0, len(mappings))
	for _, m := range mappings {
		target, err := resolveWorkspaceByName(workspaces, m.Workspace)
		if err != nil {
			return nil, fmt.Errorf("mapping %q→%q: %w", m.Folder, m.Workspace, err)
		}
		items := deploy.ItemsInFolder(all, m.Folder)
		deployed, err := client.ListItems(token, target.ID)
		if err != nil {
			return nil, fmt.Errorf("list items in %s: %w", target.DisplayName, err)
		}
		groups = append(groups, deployGroup{
			Folder:   m.Folder,
			Target:   target,
			Rows:     deploy.Compare(items, deployed, deployItemScope),
			Deployed: deployed,
		})
	}
	return groups, nil
}

// runDeploy prints the grouped compare, stops if dryRun, otherwise lets the
// user cherry-pick across groups, confirms, and executes each group against its
// own workspace. Returns the aggregated per-item results. On a mid-run Execute
// failure it returns the results accumulated so far alongside the error, so
// callers should print results before checking err.
func runDeploy(
	client deploy.FabricClient,
	token, env string,
	groups []deployGroup,
	params deploy.Parameters,
	dryRun bool,
	selectItems func([]deployGroup) (map[int][]deploy.LocalItem, error),
	confirm func(string) (bool, error),
) ([]deploy.Result, error) {
	printGroupedCompare(groups)
	if dryRun {
		return nil, nil
	}

	selected, err := selectItems(groups)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, items := range selected {
		total += len(items)
	}
	if total == 0 {
		fmt.Println("Nothing selected to deploy.")
		return nil, nil
	}

	wsCount := 0
	for _, items := range selected {
		if len(items) > 0 {
			wsCount++
		}
	}
	ok, err := confirm(fmt.Sprintf("Deploy %d item(s) across %d workspace(s)?", total, wsCount))
	if err != nil {
		return nil, err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil, nil
	}

	var allResults []deploy.Result
	for i, g := range groups {
		items := selected[i]
		if len(items) == 0 {
			continue
		}
		plan := deploy.BuildPlan(items, g.Deployed)
		sp := ui.NewSpinner(fmt.Sprintf("Publishing to %s...", g.Target.DisplayName))
		sp.Start()
		results, execErr := deploy.Execute(client, token, g.Target, env, plan, params)
		sp.Stop()
		allResults = append(allResults, results...)
		if execErr != nil {
			return allResults, execErr
		}
	}
	return allResults, nil
}

// pickEnvironment shows the customer's environment aliases as a numbered menu.
func pickEnvironment(customer config.Customer) (string, error) {
	options := make([]ui.MenuOption, len(customer.Environments))
	for i, e := range customer.Environments {
		label := e.Alias
		if len(e.Deployments) > 0 {
			label = fmt.Sprintf("%s (%d mapping%s)", e.Alias, len(e.Deployments), pluralS(len(e.Deployments)))
		}
		options[i] = ui.MenuOption{Label: label, Value: e.Alias}
	}
	return ui.NumberMenu("Select environment to deploy to", options)
}

// pickDeployMode asks whether to compare-only (dry run) or compare-and-deploy.
func pickDeployMode() (bool, error) {
	choice, err := ui.NumberMenu("What would you like to do?", []ui.MenuOption{
		{Label: "Compare only (dry run)", Value: "dry"},
		{Label: "Compare and deploy", Value: "deploy"},
	})
	if err != nil {
		return false, err
	}
	return choice == "dry", nil
}

// resolveWorkspaceByName finds a workspace by display name among those the user
// can see, with an actionable error if it's missing.
func resolveWorkspaceByName(workspaces []fabric.Workspace, name string) (fabric.Workspace, error) {
	for _, w := range workspaces {
		if w.DisplayName == name {
			return w, nil
		}
	}
	return fabric.Workspace{}, fmt.Errorf("workspace %q not found (check spelling and your access)", name)
}

// pickGroupedRows shows all groups' New/Exists rows as one checkbox list, each
// label prefixed with its target workspace. Returns the chosen LocalItems keyed
// by group index. Orphans are shown in the printed compare but excluded here —
// Phase 1 cannot deploy or delete them.
func pickGroupedRows(groups []deployGroup) (map[int][]deploy.LocalItem, error) {
	type entry struct {
		gi   int
		item deploy.LocalItem
	}
	var labels []string
	var initial []string
	byLabel := map[string]entry{}
	for gi, g := range groups {
		for _, r := range g.Rows {
			if r.Class == deploy.ClassOrphan {
				continue
			}
			label := fmt.Sprintf("%-22s %-7s %-14s %s", g.Target.DisplayName, r.Class, r.ItemType(), r.Name())
			labels = append(labels, label)
			byLabel[label] = entry{gi, r.Local}
			initial = append(initial, label)
		}
	}
	if len(labels) == 0 {
		return map[int][]deploy.LocalItem{}, nil
	}
	chosen, err := ui.MultiSelect("Select items to deploy", labels, initial)
	if err != nil {
		return nil, err
	}
	out := map[int][]deploy.LocalItem{}
	for _, l := range chosen {
		e := byLabel[l]
		out[e.gi] = append(out[e.gi], e.item)
	}
	return out, nil
}

// printGroupedCompare renders the compare result grouped by target workspace.
func printGroupedCompare(groups []deployGroup) {
	fmt.Println()
	for _, g := range groups {
		header := g.Target.DisplayName
		if g.Folder != "" {
			header = g.Folder + "/ → " + g.Target.DisplayName
		}
		fmt.Println(infoStyle.Render(header))
		if len(g.Rows) == 0 {
			fmt.Println("  (no items)")
			continue
		}
		for _, r := range g.Rows {
			fmt.Printf("  %-7s %-14s %s\n", r.Class, r.ItemType(), r.Name())
		}
	}
	fmt.Println()
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
