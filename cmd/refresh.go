package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// categorizeRefreshTable buckets tables by their typical star-schema
// prefix for the refresh-picker's group toggles. Lives in cmd/ (not
// internal/ui) because the prefix list is customer-naming-specific —
// keeping it out of the shared UI module avoids coupling internal/ui
// to one customer's conventions. The "Fakta" alias is intentional —
// the codebase serves Norwegian customers who name fact tables Fakta*.
func categorizeRefreshTable(name string) string {
	switch {
	case strings.HasPrefix(name, "Dim"):
		return "Dim"
	case strings.HasPrefix(name, "Fact"), strings.HasPrefix(name, "Fakta"):
		return "Fact"
	case strings.HasPrefix(name, "Log"):
		return "Log"
	default:
		return "Other"
	}
}

// taggedDataset pairs a semantic model with the workspace it lives in.
// We carry both so the dataset picker can disambiguate same-named
// models across workspaces, and so the refresh API call routes to the
// right workspace ID.
type taggedDataset struct {
	Dataset   fabric.Dataset
	Workspace WorkspaceRef
}

// Refresh is the top-level entry point for the `refresh` subcommand.
// Walks the user through customer → env → semantic model → tables →
// trigger, then polls until completion.
func Refresh(configPath string) error {
	return RefreshWithAPI(configPath, DefaultAPI)
}

func RefreshWithAPI(configPath string, client APIClient) error {
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
		return fmt.Errorf("no environments configured for customer %q — run 'futils edit' to add one", customerName)
	}

	env, err := selectEnvironment(customer)
	if err != nil {
		return err
	}

	workspaceNames, ok := customer.Workspaces(env)
	if !ok {
		return fmt.Errorf("alias %q has no workspace mapping — run 'futils edit'", env)
	}

	token, refs, err := authAndResolveWorkspaces(client, customerName, workspaceNames)
	if err != nil {
		return err
	}

	dsSpinner := ui.NewSpinner("Listing semantic models...")
	dsSpinner.Start()
	tagged, err := aggregateDatasets(client, token, refs)
	dsSpinner.Stop()
	if err != nil {
		return err
	}
	if len(tagged) == 0 {
		return fmt.Errorf("no semantic models found in env %q", env)
	}

	picked, err := selectDataset(tagged)
	if err != nil {
		return err
	}

	tableSpinner := ui.NewSpinner("Retrieving tables...")
	tableSpinner.Start()
	tables, err := client.QueryRefreshableTables(token, picked.Workspace.ID, picked.Dataset.ID)
	tableSpinner.Stop()
	if err != nil {
		return fmt.Errorf("query tables: %w", err)
	}
	if len(tables) == 0 {
		return fmt.Errorf("no refreshable tables found in %s", picked.Dataset.Name)
	}

	selection, err := ui.TableCheckbox("Select tables to refresh", tables, categorizeRefreshTable)
	if err != nil {
		return err
	}
	if !selection.FullRefresh && len(selection.Tables) == 0 {
		fmt.Println("No tables selected.")
		return nil
	}

	fmt.Println()
	fmt.Println(infoStyle.Render("Refresh Summary"))
	fmt.Printf("  Customer:    %s\n", customerName)
	fmt.Printf("  Environment: %s\n", env)
	fmt.Printf("  Workspace:   %s\n", picked.Workspace.Name)
	fmt.Printf("  Model:       %s\n", picked.Dataset.Name)
	fmt.Printf("  Tables:      %s\n", selection.Summary)
	fmt.Println()

	confirmed, err := ui.Confirm("Start refresh?")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	startTime := time.Now()

	spinner := ui.NewSpinner("Refreshing...")
	spinner.Start()

	var (
		refreshErr error
		status     fabric.RefreshStatus
	)
	func() {
		defer spinner.Stop()
		requestID, err := client.TriggerRefresh(token, picked.Workspace.ID, picked.Dataset.ID, selection.Tables)
		if err != nil {
			refreshErr = err
			return
		}
		status, err = client.WaitForRefresh(token, picked.Workspace.ID, picked.Dataset.ID, requestID)
		if err != nil {
			refreshErr = err
			return
		}
	}()
	if refreshErr != nil {
		return refreshErr
	}

	duration := time.Since(startTime).Round(time.Second)

	fmt.Println()
	if status.Status == "Completed" {
		fmt.Println(successStyle.Render(fmt.Sprintf("Refresh completed successfully! (%s)", duration)))
	} else {
		msg := fmt.Sprintf("Refresh %s", status.Status)
		if len(status.Messages) > 0 {
			msg += "\n"
			for _, m := range status.Messages {
				msg += fmt.Sprintf("  • %s\n", m.Message)
			}
		}
		fmt.Println(errorStyle.Render(msg))
	}
	return nil
}

// aggregateDatasets fans ListDatasets across every workspace ref,
// tagging each model with its origin workspace.
func aggregateDatasets(client APIClient, token string, refs []WorkspaceRef) ([]taggedDataset, error) {
	var all []taggedDataset
	for _, ref := range refs {
		datasets, err := client.ListDatasets(token, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list semantic models in %s: %w", ref.Name, err)
		}
		for _, ds := range datasets {
			all = append(all, taggedDataset{Dataset: ds, Workspace: ref})
		}
	}
	return all, nil
}

// selectDataset asks the user which semantic model to refresh. Always
// shows the workspace alongside the model name — when an env spans
// multiple workspaces the same model name can appear twice, and even
// when it doesn't, the workspace context tells the user where the
// refresh will actually run.
func selectDataset(tagged []taggedDataset) (taggedDataset, error) {
	if len(tagged) == 1 {
		return tagged[0], nil
	}
	// Compute the longest model name so the workspace column lines up.
	// Rune count, not byte length — Norwegian Æ/Ø/Å occupy 2 bytes each.
	maxName := 0
	for _, t := range tagged {
		if n := utf8.RuneCountInString(t.Dataset.Name); n > maxName {
			maxName = n
		}
	}
	options := make([]ui.MenuOption, len(tagged))
	key := func(t taggedDataset) string { return t.Workspace.ID + "/" + t.Dataset.ID }
	byKey := map[string]taggedDataset{}
	for i, t := range tagged {
		label := fmt.Sprintf("%s  %s → %s",
			t.Dataset.Name,
			strings.Repeat(" ", maxName-utf8.RuneCountInString(t.Dataset.Name)),
			t.Workspace.Name,
		)
		options[i] = ui.MenuOption{Label: label, Value: key(t)}
		byKey[key(t)] = t
	}
	selected, err := ui.NumberMenu("Select semantic model", options)
	if err != nil {
		return taggedDataset{}, err
	}
	return byKey[selected], nil
}
