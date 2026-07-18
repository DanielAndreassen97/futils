package cmd

import (
	"fmt"
	"strconv"
	"time"

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

	// Fan ListItemsByType across the env's workspaces, tagging each pipeline
	// with its origin — same aggregation shape as the notebook picker.
	spinner := ui.NewSpinner("Listing pipelines...")
	spinner.Start()
	type taggedPipeline struct {
		Pipeline  fabric.Item
		Workspace WorkspaceRef
	}
	var pipelines []taggedPipeline
	var listErr error
	for _, ref := range refs {
		items, err := client.ListItemsByType(token, ref.ID, "DataPipeline")
		if err != nil {
			listErr = fmt.Errorf("list pipelines in %s: %w", ref.Name, err)
			break
		}
		for _, it := range items {
			pipelines = append(pipelines, taggedPipeline{Pipeline: it, Workspace: ref})
		}
	}
	spinner.Stop()
	if listErr != nil {
		return listErr
	}
	if len(pipelines) == 0 {
		fmt.Println(infoStyle.Render("No data pipelines found in this environment's workspaces."))
		return nil
	}

	multiWS := len(refs) > 1
	opts := make([]ui.FilterOption, len(pipelines))
	for i, p := range pipelines {
		label := p.Pipeline.DisplayName
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
	fmt.Printf("  Pipeline:    %s\n", picked.Pipeline.DisplayName)
	fmt.Println()

	confirmed, err := ui.Confirm("Start pipeline run?")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	startTime := time.Now()
	sp := ui.NewSpinner("Pipeline is running...")
	sp.Start()
	var status fabric.JobInstanceStatus
	var runErr error
	func() {
		defer sp.Stop()
		instanceURL, err := client.RunPipeline(token, picked.Workspace.ID, picked.Pipeline.ID)
		if err != nil {
			runErr = fmt.Errorf("submit job: %w", err)
			return
		}
		status, runErr = pollJob(client, token, instanceURL)
	}()
	if runErr != nil {
		return runErr
	}

	duration := time.Since(startTime).Round(time.Second)
	fmt.Println()
	switch status.Status {
	case fabric.JobStatusCompleted:
		fmt.Println(successStyle.Render(fmt.Sprintf("Pipeline completed successfully! (%s)", duration)))
	default:
		msg := fmt.Sprintf("Pipeline %s (%s)", status.Status, duration)
		if status.FailureReason != nil {
			msg += fmt.Sprintf("\n  %v", status.FailureReason)
		}
		fmt.Println(errorStyle.Render(msg))
	}
	return nil
}
