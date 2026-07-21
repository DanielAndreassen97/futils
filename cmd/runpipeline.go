package cmd

import (
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// RunPipelineCmd is the `runpipeline` entry point: pick a customer,
// environment and data pipeline, override its parameters in a pre-filled form,
// then submit a pipeline job and poll it to a terminal state — the pipeline
// twin of the Run (notebook) flow.
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

	// Read the pipeline's declared parameters and their defaults, then let the
	// user override them in a pre-filled form. Omitted/unchanged params keep the
	// pipeline's own defaults (Fabric applies them server-side), so the form
	// only sends what actually changed. Best-effort: a definition that can't be
	// fetched or parsed just runs the pipeline with no overrides.
	params := pipelineParamOverrides(client, token, picked.Workspace.ID, picked.Item)

	fmt.Println()
	fmt.Println(infoStyle.Render("Run summary"))
	fmt.Printf("  Customer:    %s\n", customerName)
	fmt.Printf("  Environment: %s\n", env)
	fmt.Printf("  Workspace:   %s\n", picked.Workspace.Name)
	fmt.Printf("  Pipeline:    %s\n", picked.Item.DisplayName)
	if len(params) > 0 {
		fmt.Printf("  Parameters:  %s\n", describePipelineParams(params))
	}
	fmt.Println()

	return runJobAndReport(client, token, "Pipeline", func() (string, error) {
		return client.RunPipeline(token, picked.Workspace.ID, picked.Item.ID, params)
	})
}

// pipelineParamOverrides fetches a pipeline's definition, parses its declared
// parameters, and — when it has any — shows a pre-filled form so the user can
// override the defaults. Returns the flat name→value map of CHANGED values
// (RunPipeline's shape); nil when the pipeline has no parameters, the user
// changed nothing, or the definition couldn't be read (the run then falls back
// to the pipeline's own defaults). Never fails the run: parse/definition
// problems degrade to no overrides with a note.
func pipelineParamOverrides(client APIClient, token, workspaceID string, item fabric.Item) map[string]any {
	def, err := client.GetItemDefinition(token, workspaceID, item.ID, "")
	if err != nil {
		fmt.Println(infoStyle.Render("Couldn't read pipeline parameters — running with its defaults."))
		return nil
	}
	var content []byte
	for _, p := range def.Parts {
		if path.Base(p.Path) == "pipeline-content.json" {
			if b, derr := base64.StdEncoding.DecodeString(p.Payload); derr == nil {
				content = b
			}
			break
		}
	}
	if content == nil {
		return nil
	}
	params, err := fabric.ParsePipelineParameters(content)
	if err != nil || len(params) == 0 {
		return nil
	}

	overrides, err := ui.ParameterForm(params, true)
	if err != nil {
		return nil // esc/ctrl+c out of the form — run with defaults
	}
	if len(overrides) == 0 {
		fmt.Println(infoStyle.Render("All parameters left at their pipeline defaults."))
		return nil
	}
	out := make(map[string]any, len(overrides))
	for _, o := range overrides {
		out[o.Name] = o.Value
	}
	return out
}

// describePipelineParams renders the changed pipeline parameters as a compact
// name=value list for the run summary, sorted for a stable line.
func describePipelineParams(params map[string]any) string {
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s=%v", n, params[n])
	}
	return strings.Join(parts, ", ")
}
