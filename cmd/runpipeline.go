package cmd

import (
	"fmt"
	"strconv"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// RunPipelineCmd is the `runpipeline` entry point: pick a customer,
// environment and data pipeline, then submit a pipeline job and poll it to a
// terminal state — the pipeline twin of the Run (notebook) flow, minus the
// parameter form (pipeline runs take no overrides here yet).
func RunPipelineCmd(configPath string) error {
	return RunPipelineWithAPI(configPath, DefaultAPI)
}

func RunPipelineWithAPI(configPath string, client APIClient) error {
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

	pipelines, err := aggregateItems(refs, "pipelines", func(wsID string) ([]fabric.Item, error) {
		return client.ListItemsByType(token, wsID, "DataPipeline")
	})
	if err != nil {
		return err
	}
	if len(pipelines) == 0 {
		fmt.Println(infoStyle.Render("No data pipelines found in this environment's workspaces."))
		return nil
	}

	multiWS := len(refs) > 1
	opts := make([]ui.FilterOption, len(pipelines))
	for i, p := range pipelines {
		label := p.Item.DisplayName
		if multiWS {
			label += "  → " + p.Workspace.Name
		}
		opts[i] = ui.FilterOption{Label: label, Value: fmt.Sprint(i)}
	}
	choice, err := ui.FilterMenu("Select pipeline to run", opts, ui.DefaultFilterRowRenderer)
	if err != nil {
		return err
	}
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(pipelines) {
		return fmt.Errorf("unexpected picker value %q", choice)
	}
	picked := pipelines[idx]

	fmt.Println()
	fmt.Println(infoStyle.Render("Run summary"))
	fmt.Printf("  Customer:    %s\n", customerName)
	fmt.Printf("  Environment: %s\n", env)
	fmt.Printf("  Workspace:   %s\n", picked.Workspace.Name)
	fmt.Printf("  Pipeline:    %s\n", picked.Item.DisplayName)
	fmt.Println()

	return runJobAndReport(client, token, "Pipeline", func() (string, error) {
		return client.RunPipeline(token, picked.Workspace.ID, picked.Item.ID)
	})
}
