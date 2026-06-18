package cmd

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// deployItemScope is the set of types Phase 1 publishes / flags as orphans.
var deployItemScope = map[string]bool{
	"Notebook": true, "DataPipeline": true, "SemanticModel": true, "Report": true,
}

// comparableTypes are the item types futils content-diffs (mirrors fabric-cicd's
// COMPARABLE_TYPES). Other existing types are shown as Exists rather than diffed —
// they're shell types or lack a reliable normalizer yet.
var comparableTypes = map[string]bool{"Notebook": true, "DataPipeline": true}

// deployGroup is one folder→workspace mapping resolved for a run: the items
// discovered under that folder, the target workspace, the compare rows, and the
// deployed item list (needed by BuildPlan).
type deployGroup struct {
	Folder     string
	Target     fabric.Workspace
	Rows       []deploy.CompareRow
	Deployed   []fabric.Item
	Params     deploy.Parameters
	Unresolved []deploy.UnresolvedRef
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
	pickedRepo := repoPath == ""
	if pickedRepo {
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
	// Remember the repo on the customer so the picker is skipped next time.
	if pickedRepo {
		customer.RepoPath = src.Repo()
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			fmt.Println(warningStyle.Render("Couldn't save repo path: " + err.Error()))
		} else {
			fmt.Println(infoStyle.Render(fmt.Sprintf("Saved repo path for %s: %s", customerName, src.Repo())))
		}
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

	rebinder, err := buildRebinder(client, token, customer, alias, workspaces)
	if err != nil {
		return fmt.Errorf("set up reference rebinding: %w", err)
	}
	if rebinder == nil {
		fmt.Println(infoStyle.Render("Auto-rebind disabled (no baseline environment set). Set one via Edit customer to translate references by name."))
	}

	mappings, _ := customer.DeployMappings(alias)
	if len(mappings) == 0 {
		mappings, err = setupDeployMappings(all, workspaces)
		if err != nil {
			return err
		}
		if len(mappings) == 0 {
			return fmt.Errorf("no folder mappings configured for %q — nothing to deploy", alias)
		}
		if idx := findEnvIndex(customer, alias); idx >= 0 {
			customer.Environments[idx].Deployments = mappings
			if err := config.EditCustomer(configPath, customerName, customer); err != nil {
				return fmt.Errorf("save deployment mappings: %w", err)
			}
			fmt.Println(infoStyle.Render(fmt.Sprintf("Saved %d mapping(s) to env %q.", len(mappings), alias)))
		}
	}

	mappings, err = pickDeployScope(mappings)
	if err != nil {
		return err
	}

	env := alias

	groups, err := buildDeployGroups(client, token, mappings, all, workspaces, env, src, rebinder)
	if err != nil {
		return err
	}

	dryRun, err := pickDeployMode()
	if err != nil {
		return err
	}

	results, err := runDeploy(client, token, env, groups, dryRun, rebinder, pickGroupedRows, ui.Confirm)
	printDeployResults(results)
	if err != nil {
		return err
	}
	return nil
}

const diffConcurrency = 4

// buildDeployGroups turns each folder→workspace mapping into a compare group:
// items under that folder vs the mapped workspace's deployed items. For items
// that already exist it runs a content-diff (concurrent definition fetches +
// per-part normalized comparison) to refine ClassExists into ClassChanged or
// ClassUnchanged; items it can't verify stay ClassExists.
func buildDeployGroups(client APIClient, token string, mappings []config.DeployMapping, all []deploy.LocalItem, workspaces []fabric.Workspace, env string, src *deploy.Source, rb *deploy.Rebinder) ([]deployGroup, error) {
	groups := make([]deployGroup, 0, len(mappings))
	for _, m := range mappings {
		target, err := resolveWorkspaceByName(workspaces, m.Workspace)
		if err != nil {
			return nil, fmt.Errorf("mapping %q→%q: %w", m.Folder, m.Workspace, err)
		}

		// parameter.yml lives at the deployment-folder root (fabric-cicd's
		// repository_directory), e.g. FabricBackEnd/parameter.yml — NOT the git root.
		var params deploy.Parameters
		paramPath := path.Join(m.Folder, "parameter.yml")
		if raw, perr := src.ReadFile(paramPath); perr == nil {
			params, err = deploy.ParseParameters(raw)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", paramPath, err)
			}
		} else {
			fmt.Println(warningStyle.Render(fmt.Sprintf("No %s found — comparing %s/ without substitution (GUIDs may differ across environments).", paramPath, m.Folder)))
		}

		items := deploy.ItemsInFolder(all, m.Folder)
		deployed, err := client.ListItems(token, target.ID)
		if err != nil {
			return nil, fmt.Errorf("list items in %s: %w", target.DisplayName, err)
		}
		rows := deploy.Compare(items, deployed, deployItemScope)
		g := deployGroup{
			Folder:   m.Folder,
			Target:   target,
			Rows:     rows,
			Deployed: deployed,
			Params:   params,
		}
		g.Unresolved = diffExistingRows(client, token, target, env, params, rows, rb)
		groups = append(groups, g)
	}
	return groups, nil
}

// diffExistingRows fetches the deployed definition of every ClassExists row
// (concurrently, bounded) and reclassifies it ClassChanged or ClassUnchanged by
// comparing against the local item's substituted parts. Rows whose definition
// can't be fetched or substituted stay ClassExists (unverified) and a warning
// is printed with the count and first reason. Mutates rows in place.
func diffExistingRows(client APIClient, token string, target fabric.Workspace, env string, params deploy.Parameters, rows []deploy.CompareRow, rb *deploy.Rebinder) []deploy.UnresolvedRef {
	var existsIdx []int
	for i := range rows {
		if rows[i].Class == deploy.ClassExists && comparableTypes[rows[i].ItemType()] {
			existsIdx = append(existsIdx, i)
		}
	}
	if len(existsIdx) == 0 {
		return nil
	}

	sp := ui.NewSpinner(fmt.Sprintf("Comparing %d item(s) in %s...", len(existsIdx), target.DisplayName))
	sp.Start()
	type fetched struct {
		def *fabric.Definition
		err error
	}
	results := make([]fetched, len(existsIdx))
	sem := make(chan struct{}, diffConcurrency)
	var wg sync.WaitGroup
	for j, idx := range existsIdx {
		wg.Add(1)
		sem <- struct{}{}
		go func(j, idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			def, err := client.GetItemDefinition(token, target.ID, rows[idx].DeployedID, "")
			results[j] = fetched{def: def, err: err}
		}(j, idx)
	}
	wg.Wait()
	sp.Stop()

	// Map each source logicalId to its deployed GUID so cross-item references in
	// the local definition match what's live in the workspace.
	compareIDs := map[string]string{}
	for _, r := range rows {
		if r.Class == deploy.ClassExists && r.Local.LogicalID != "" {
			compareIDs[r.Local.LogicalID] = r.DeployedID
		}
	}
	resolver := deploy.NewResolver(client, token, target)

	var unverified int
	var firstErr error
	var unresolved []deploy.UnresolvedRef
	for j, idx := range existsIdx {
		if results[j].err != nil {
			unverified++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rows[idx].Name(), results[j].err)
			}
			continue
		}
		localParts, un, perr := deploy.SubstituteParts(rows[idx].Local, env, params, compareIDs, resolver, rb)
		unresolved = append(unresolved, un...)
		if perr != nil {
			unverified++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rows[idx].Name(), perr)
			}
			continue
		}
		if deploy.PartsChanged(localParts, results[j].def) {
			rows[idx].Class = deploy.ClassChanged
		} else {
			rows[idx].Class = deploy.ClassUnchanged
		}
	}
	if unverified > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf(
			"%d of %d item(s) in %s couldn't be content-compared (shown as Exists). First reason: %v",
			unverified, len(existsIdx), target.DisplayName, firstErr)))
	}
	return unresolved
}

// setupDeployMappings asks the user which workspace each repo folder deploys to,
// using the folders discovered in the repo as the pick-list. Folders the user
// skips are left unmapped. Returns the chosen mappings (possibly empty).
func setupDeployMappings(all []deploy.LocalItem, workspaces []fabric.Workspace) ([]config.DeployMapping, error) {
	folders := deploy.TopLevelFolders(all)
	if len(folders) == 0 {
		return nil, fmt.Errorf("couldn't detect any folders to map in the repo — add mappings via Edit customer instead")
	}
	const skipValue = "\x00skip"
	fmt.Println(infoStyle.Render("Set up which repo folder deploys to which workspace:"))
	var mappings []config.DeployMapping
	for _, folder := range folders {
		opts := []ui.FilterOption{{Label: "⋯ Skip this folder", Value: skipValue}}
		for _, w := range workspaces {
			opts = append(opts, ui.FilterOption{Label: w.DisplayName, Value: w.DisplayName})
		}
		chosen, err := ui.FilterMenu(fmt.Sprintf("Deploy %s/ to which workspace?", folder), opts, ui.DefaultFilterRowRenderer)
		if err != nil {
			return nil, err
		}
		if chosen == skipValue {
			continue
		}
		mappings = append(mappings, config.DeployMapping{Folder: folder, Workspace: chosen})
		fmt.Printf("  %s/ → %s\n", folder, chosen)
	}
	return mappings, nil
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
	dryRun bool,
	rb *deploy.Rebinder,
	selectItems func([]deployGroup) (map[int][]deploy.LocalItem, error),
	confirm func(string) (bool, error),
) ([]deploy.Result, error) {
	printGroupedCompare(groups)
	printUnresolved(groups)
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
		results, execErr := deploy.Execute(client, token, g.Target, env, plan, g.Params, rb)
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
			label := fmt.Sprintf("%-22s %-9s %-14s %s", g.Target.DisplayName, r.Class, r.ItemType(), r.Name())
			labels = append(labels, label)
			byLabel[label] = entry{gi, r.Local}
			if r.Class != deploy.ClassUnchanged {
				initial = append(initial, label)
			}
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

// classStyle colors a compare row by its classification: green=new,
// yellow=changed, grey=unchanged, red=orphan, cyan=exists-but-unverified.
func classStyle(c deploy.Class) lipgloss.Style {
	switch c {
	case deploy.ClassNew:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	case deploy.ClassChanged:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	case deploy.ClassUnchanged:
		return lipgloss.NewStyle().Foreground(ui.DimColor)
	case deploy.ClassOrphan:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	default: // ClassExists (unverified)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	}
}

// printGroupedCompare renders the compare result grouped by target workspace,
// colored by classification with a legend.
func printGroupedCompare(groups []deployGroup) {
	fmt.Println()
	legend := classStyle(deploy.ClassNew).Render("New") + "  " +
		classStyle(deploy.ClassChanged).Render("Changed") + "  " +
		classStyle(deploy.ClassUnchanged).Render("Unchanged") + "  " +
		classStyle(deploy.ClassOrphan).Render("Orphan") + "  " +
		classStyle(deploy.ClassExists).Render("Exists")
	fmt.Println("  " + legend)
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
			line := fmt.Sprintf("  %-9s %-14s %s", r.Class, r.ItemType(), r.Name())
			fmt.Println(classStyle(r.Class).Render(line))
		}
	}
	fmt.Println()
}

// printUnresolved lists reference GUIDs the rebinder could not translate, with
// enough context for the user to register an override (or ignore/strip them).
// Silent when everything resolved.
func printUnresolved(groups []deployGroup) {
	var total int
	for _, g := range groups {
		total += len(g.Unresolved)
	}
	if total == 0 {
		return
	}
	fmt.Println()
	fmt.Println(warningStyle.Render(fmt.Sprintf("%d unresolved reference(s) — left as-is. Register an override (Edit customer) to map them by name:", total)))
	for _, g := range groups {
		for _, u := range g.Unresolved {
			short := u.GUID
			if len(short) > 8 {
				short = short[:8] + "…"
			}
			fmt.Printf("  %s in %s — looks like a %s (%s)\n", short, u.ItemName, u.ItemType, u.Location)
		}
	}
	fmt.Println()
}

// pickDeployScope lets the user deploy a single folder→workspace mapping or all
// of them. With one mapping it's a no-op. Returns the chosen subset.
func pickDeployScope(mappings []config.DeployMapping) ([]config.DeployMapping, error) {
	if len(mappings) <= 1 {
		return mappings, nil
	}
	opts := []ui.MenuOption{{Label: fmt.Sprintf("All (%d mappings)", len(mappings)), Value: "__all"}}
	for i, m := range mappings {
		opts = append(opts, ui.MenuOption{Label: fmt.Sprintf("%s/ → %s", m.Folder, m.Workspace), Value: fmt.Sprintf("%d", i)})
	}
	choice, err := ui.NumberMenu("Deploy which folder?", opts)
	if err != nil {
		return nil, err
	}
	if choice == "__all" {
		return mappings, nil
	}
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(mappings) {
		return nil, fmt.Errorf("invalid selection %q", choice)
	}
	return []config.DeployMapping{mappings[idx]}, nil
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
