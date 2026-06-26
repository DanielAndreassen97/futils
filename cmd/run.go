package cmd

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// menuValueShowAll is the sentinel used in the favourites-filtered menu
// to jump to the full notebook list. Prefixed with __ so it can't
// collide with a real notebook UUID.
const menuValueShowAll = "__showall"

// showAllLabel is the "Show all notebooks" entry in the favourites-filtered
// menu. Leading ellipsis dots give it visual separation from real names.
const showAllLabel = "⋯ Show all notebooks"

// Styled message boxes used for the final job-result presentation.
var (
	successStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("2")).
			Foreground(lipgloss.Color("2")).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("1")).
			Foreground(lipgloss.Color("1")).
			Padding(0, 1)

	warningStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("3")).
			Foreground(lipgloss.Color("3")).
			Padding(0, 1)

	infoStyle = lipgloss.NewStyle().Foreground(ui.AccentColor)
)

// APIClient abstracts the Fabric calls the run flow needs. Lets us swap
// in a fake for tests or demo mode without touching command logic.
type APIClient interface {
	GetAccessToken(profile string) (string, error)
	GetWorkspaceID(token, workspaceName string) (string, error)
	ListWorkspaces(token string) ([]fabric.Workspace, error)
	ListNotebooks(token, workspaceID string) ([]fabric.Item, error)
	GetNotebookIpynb(token, workspaceID, itemID string) ([]byte, error)
	RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error)
	GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error)

	// Move flow.
	ListItems(token, workspaceID string) ([]fabric.Item, error)
	ListItemsByType(token, workspaceID, itemType string) ([]fabric.Item, error)
	GetItemDefinition(token, workspaceID, itemID, format string) (*fabric.Definition, error)
	CreateItem(token, workspaceID, displayName, itemType string, def *fabric.Definition) (fabric.Item, error)
	UpdateItemDefinition(token, workspaceID, itemID string, def *fabric.Definition) error
	UpdateItem(token, workspaceID, itemID, displayName, description string) error
	DeleteItem(token, workspaceID, itemID string) error
	RebindReport(token, workspaceID, reportID, datasetID string) error

	// Refresh flow.
	ListDatasets(token, workspaceID string) ([]fabric.Dataset, error)
	QueryRefreshableTables(token, workspaceID, datasetID string) ([]string, error)
	TriggerRefresh(token, workspaceID, datasetID string, tables []string) (string, error)
	WaitForRefresh(token, workspaceID, datasetID, requestID string) (fabric.RefreshStatus, error)

	// Deploy flow.
	GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (host, id string, err error)
	BulkImportDefinitions(token, workspaceID string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error)
}

// RealAPIClient just forwards to the internal/fabric package functions.
type RealAPIClient struct{}

func (RealAPIClient) GetAccessToken(profile string) (string, error) {
	return fabric.GetAccessToken(profile)
}
func (RealAPIClient) GetWorkspaceID(token, workspaceName string) (string, error) {
	return fabric.GetWorkspaceID(token, workspaceName)
}
func (RealAPIClient) ListWorkspaces(token string) ([]fabric.Workspace, error) {
	return fabric.ListWorkspaces(token)
}
func (RealAPIClient) ListNotebooks(token, workspaceID string) ([]fabric.Item, error) {
	return fabric.ListNotebooks(token, workspaceID)
}
func (RealAPIClient) GetNotebookIpynb(token, workspaceID, itemID string) ([]byte, error) {
	return fabric.GetNotebookIpynb(token, workspaceID, itemID)
}
func (RealAPIClient) RunNotebook(token, workspaceID, itemID string, inputs []fabric.JobInput, lakehouse *fabric.DefaultLakehouse) (string, error) {
	return fabric.RunNotebook(token, workspaceID, itemID, inputs, lakehouse)
}
func (RealAPIClient) GetJobInstance(token, instanceURL string) (fabric.JobInstanceStatus, error) {
	return fabric.GetJobInstance(token, instanceURL)
}
func (RealAPIClient) ListItems(token, workspaceID string) ([]fabric.Item, error) {
	return fabric.ListItems(token, workspaceID)
}
func (RealAPIClient) ListItemsByType(token, workspaceID, itemType string) ([]fabric.Item, error) {
	return fabric.ListItemsByType(token, workspaceID, itemType)
}
func (RealAPIClient) GetItemDefinition(token, workspaceID, itemID, format string) (*fabric.Definition, error) {
	return fabric.GetItemDefinition(token, workspaceID, itemID, format)
}
func (RealAPIClient) CreateItem(token, workspaceID, displayName, itemType string, def *fabric.Definition) (fabric.Item, error) {
	return fabric.CreateItem(token, workspaceID, displayName, itemType, def)
}
func (RealAPIClient) UpdateItemDefinition(token, workspaceID, itemID string, def *fabric.Definition) error {
	return fabric.UpdateItemDefinition(token, workspaceID, itemID, def)
}
func (RealAPIClient) UpdateItem(token, workspaceID, itemID, displayName, description string) error {
	return fabric.UpdateItem(token, workspaceID, itemID, displayName, description)
}
func (RealAPIClient) DeleteItem(token, workspaceID, itemID string) error {
	return fabric.DeleteItem(token, workspaceID, itemID)
}
func (RealAPIClient) RebindReport(token, workspaceID, reportID, datasetID string) error {
	return fabric.RebindReport(token, workspaceID, reportID, datasetID)
}
func (RealAPIClient) ListDatasets(token, workspaceID string) ([]fabric.Dataset, error) {
	return fabric.ListDatasets(token, workspaceID)
}
func (RealAPIClient) QueryRefreshableTables(token, workspaceID, datasetID string) ([]string, error) {
	return fabric.QueryRefreshableTables(token, workspaceID, datasetID)
}
func (RealAPIClient) TriggerRefresh(token, workspaceID, datasetID string, tables []string) (string, error) {
	return fabric.TriggerRefresh(token, workspaceID, datasetID, tables)
}
func (RealAPIClient) WaitForRefresh(token, workspaceID, datasetID, requestID string) (fabric.RefreshStatus, error) {
	return fabric.WaitForRefresh(token, workspaceID, datasetID, requestID, 5*time.Second, 30*time.Minute)
}
func (RealAPIClient) GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID string) (string, string, error) {
	return fabric.GetLakehouseSqlEndpoint(token, workspaceID, lakehouseID)
}
func (RealAPIClient) BulkImportDefinitions(token, workspaceID string, parts []fabric.DefinitionPart, opts fabric.BulkImportOptions) (*fabric.BulkImportResult, error) {
	return fabric.BulkImportDefinitions(token, workspaceID, parts, opts)
}

// DefaultAPI is what Run() uses when called from the main menu. Test code
// can point it at a fake before invoking RunWithAPI.
var DefaultAPI APIClient = RealAPIClient{}

// Run is the top-level entry point for the `run` subcommand.
func Run(configPath string) error {
	return RunWithAPI(configPath, DefaultAPI)
}

// RunWithAPI walks the full user flow: pick customer → env → notebook,
// fill out the parameter form, submit, and poll to completion. Tests
// drive this directly with a fake APIClient.
func RunWithAPI(configPath string, client APIClient) error {
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

	tagged, err := aggregateNotebooks(client, token, refs)
	if err != nil {
		return err
	}

	picked, err := selectNotebookWithFavorites(tagged, customer)
	if err != nil {
		return err
	}
	notebook := picked.Notebook
	workspaceName := picked.Workspace.Name
	workspaceID := picked.Workspace.ID

	defSpinner := ui.NewSpinner("Fetching notebook definition...")
	defSpinner.Start()
	ipynb, err := client.GetNotebookIpynb(token, workspaceID, notebook.ID)
	defSpinner.Stop()
	if err != nil {
		return fmt.Errorf("get notebook definition: %w", err)
	}

	params, err := fabric.ParseParameters(ipynb)
	if err != nil {
		return fmt.Errorf("parse parameters: %w", err)
	}

	// Filter params to the favourited subset (if any) so the form only
	// asks about what this user actually tunes. Unpinned params fall back
	// to their notebook-declared defaults automatically — same mechanism
	// as "leave input empty to keep default" in the form itself.
	shownParams := filterParamsByFavorite(params, customer, notebook.DisplayName)

	var overrides []fabric.JobInput
	if len(shownParams) > 0 {
		overrides, err = ui.ParameterForm(shownParams)
		if err != nil {
			return err
		}
	} else if len(params) > 0 {
		fmt.Println(infoStyle.Render("All parameters left at notebook defaults (none pinned as favourites)."))
	} else {
		fmt.Println(infoStyle.Render("Notebook has no parameters cell — will run with no overrides."))
	}

	// Detect and repair a broken default-lakehouse binding: a notebook that
	// pins a lakehouse GUID but ships an empty workspace id fails a headless
	// submit at session attach ("LakehouseWorkspaceId is not a valid GUID").
	// We resolve the lakehouse's real home workspace and pass it as a per-run
	// override. Notebooks with a complete binding — or none at all — are left
	// untouched; futils only intervenes for this one pattern.
	lakehouse, err := resolveLakehouseOverride(client, token, ipynb, workspaceID)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(infoStyle.Render("Run summary"))
	fmt.Printf("  Customer:    %s\n", customerName)
	fmt.Printf("  Environment: %s\n", env)
	fmt.Printf("  Workspace:   %s\n", workspaceName)
	fmt.Printf("  Notebook:    %s\n", notebook.DisplayName)
	fmt.Printf("  Overrides:   %s\n", describeOverrides(overrides))
	if lakehouse != nil {
		fmt.Printf("  Lakehouse:   %s (binding had no workspace — resolved to %s)\n", lakehouse.Name, lakehouse.WorkspaceID)
	}
	fmt.Println()

	confirmed, err := ui.Confirm("Start notebook run?")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	startTime := time.Now()

	// Single spinner covers both the initial submit and all polling.
	// Polling happens silently; the user only sees a result box once
	// the job reaches a terminal state.
	spinner := ui.NewSpinner("Notebook is running...")
	spinner.Start()

	var runErr error
	var status fabric.JobInstanceStatus

	func() {
		defer spinner.Stop()

		instanceURL, err := client.RunNotebook(token, workspaceID, notebook.ID, overrides, lakehouse)
		if err != nil {
			runErr = fmt.Errorf("submit job: %w", err)
			return
		}

		status, err = pollJob(client, token, instanceURL)
		if err != nil {
			runErr = err
			return
		}
	}()

	if runErr != nil {
		return runErr
	}

	duration := time.Since(startTime).Round(time.Second)
	fmt.Println()
	switch status.Status {
	case fabric.JobStatusCompleted:
		fmt.Println(successStyle.Render(fmt.Sprintf("Notebook completed successfully! (%s)", duration)))
	default:
		msg := fmt.Sprintf("Notebook %s (%s)", status.Status, duration)
		if status.FailureReason != nil {
			msg += fmt.Sprintf("\n  %v", status.FailureReason)
		}
		fmt.Println(errorStyle.Render(msg))
	}
	return nil
}

// pollJob blocks until the job instance reaches a terminal state. Silent
// by design — the caller runs it under a spinner, so status-transition
// prints would mangle the animation. If you need per-status visibility
// for debugging, use `cmd/fetch-nb -run` which prints each transition.
func pollJob(client APIClient, token, instanceURL string) (fabric.JobInstanceStatus, error) {
	const pollInterval = 5 * time.Second
	const timeout = 2 * time.Hour

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := client.GetJobInstance(token, instanceURL)
		if err != nil {
			return status, fmt.Errorf("poll job: %w", err)
		}
		if status.IsTerminal() {
			return status, nil
		}
		time.Sleep(pollInterval)
	}
	return fabric.JobInstanceStatus{}, fmt.Errorf("notebook did not complete within %s", timeout)
}

// selectCustomer auto-picks when only one customer exists, otherwise
// shows a menu.
func selectCustomer(cfg config.Config) (string, config.Customer, error) {
	names := sortedCustomerNames(cfg)
	if len(names) == 1 {
		name := names[0]
		return name, cfg.Customers[name], nil
	}
	selected, err := ui.NumberMenu("Select customer", ui.MenuOptionsFromStrings(names))
	if err != nil {
		return "", config.Customer{}, err
	}
	return selected, cfg.Customers[selected], nil
}

func selectEnvironment(customer config.Customer) (string, error) {
	if len(customer.Environments) == 1 {
		return customer.Environments[0].Alias, nil
	}
	options := make([]ui.MenuOption, len(customer.Environments))
	for i, e := range customer.Environments {
		options[i] = ui.MenuOption{Label: e.Alias, Value: e.Alias}
	}
	return ui.NumberMenu("Select environment", options)
}

// notebookKey uniquely identifies a TaggedNotebook for use as a menu
// option value — workspace ID + notebook ID, since the same notebook ID
// can't repeat within a workspace but the same notebook name *can*
// repeat across workspaces.
func notebookKey(t TaggedNotebook) string {
	return t.Workspace.ID + "/" + t.Notebook.ID
}

// selectNotebook is the full notebook picker (no favourite filtering).
// Always shows the workspace alongside the notebook name so the user
// knows where the run will land.
func selectNotebook(tagged []TaggedNotebook) (TaggedNotebook, error) {
	if len(tagged) == 1 {
		return tagged[0], nil
	}
	maxName := 0
	for _, t := range tagged {
		// Rune count, not byte length — Norwegian display names with Æ/Ø/Å
		// (2 bytes each in UTF-8) would otherwise misalign the workspace
		// column. lipgloss counts runes for printable width as well.
		if n := utf8.RuneCountInString(t.Notebook.DisplayName); n > maxName {
			maxName = n
		}
	}
	options := make([]ui.MenuOption, len(tagged))
	byKey := map[string]TaggedNotebook{}
	for i, t := range tagged {
		label := fmt.Sprintf("%s  %s → %s",
			t.Notebook.DisplayName,
			strings.Repeat(" ", maxName-utf8.RuneCountInString(t.Notebook.DisplayName)),
			t.Workspace.Name,
		)
		options[i] = ui.MenuOption{Label: label, Value: notebookKey(t)}
		byKey[notebookKey(t)] = t
	}
	selected, err := ui.NumberMenu("Select notebook", options)
	if err != nil {
		return TaggedNotebook{}, err
	}
	return byKey[selected], nil
}

// selectNotebookWithFavorites narrows the notebook menu to the
// customer's favourites when any are configured, with a "Show all"
// escape hatch appended as the last option. Customers with no
// favourites see the full list — zero UI churn for first-time users.
//
// Favourites match by display name across all workspaces in the env;
// if the same notebook name exists in two workspaces, both surface as
// favourites and the workspace suffix disambiguates them in the menu.
func selectNotebookWithFavorites(tagged []TaggedNotebook, customer config.Customer) (TaggedNotebook, error) {
	if len(customer.Favorites) == 0 {
		return selectNotebook(tagged)
	}

	// Group tagged notebooks by display name so we can preserve the
	// user-curated order from customer.Favorites instead of whatever
	// order ListNotebooks returned them in.
	byName := make(map[string][]TaggedNotebook, len(tagged))
	byKey := make(map[string]TaggedNotebook, len(tagged))
	for _, t := range tagged {
		byKey[notebookKey(t)] = t
		byName[t.Notebook.DisplayName] = append(byName[t.Notebook.DisplayName], t)
	}

	var favTagged []TaggedNotebook
	for _, fav := range customer.Favorites {
		favTagged = append(favTagged, byName[fav.Name]...)
	}

	// Fallback: all favourites are stale. Degrade to full list rather
	// than showing an empty menu.
	if len(favTagged) == 0 {
		return selectNotebook(tagged)
	}

	maxName := 0
	for _, t := range favTagged {
		if n := utf8.RuneCountInString(t.Notebook.DisplayName); n > maxName {
			maxName = n
		}
	}

	options := make([]ui.MenuOption, 0, len(favTagged)+1)
	for _, t := range favTagged {
		label := fmt.Sprintf("%s  %s → %s",
			t.Notebook.DisplayName,
			strings.Repeat(" ", maxName-utf8.RuneCountInString(t.Notebook.DisplayName)),
			t.Workspace.Name,
		)
		options = append(options, ui.MenuOption{Label: label, Value: notebookKey(t)})
	}
	options = append(options, ui.MenuOption{
		Label: fmt.Sprintf("%s (%d total)", showAllLabel, len(tagged)),
		Value: menuValueShowAll,
	})

	selected, err := ui.NumberMenu("Select notebook", options)
	if err != nil {
		return TaggedNotebook{}, err
	}
	if selected == menuValueShowAll {
		return selectNotebook(tagged)
	}
	return byKey[selected], nil
}

// filterParamsByFavorite returns the subset of params the user has
// pinned for this notebook, preserving notebook-declared order. Returns
// the full list when the notebook has no favourite or its Parameters
// slice is empty (the "show all" signal from the favourites flow).
func filterParamsByFavorite(params []fabric.Parameter, customer config.Customer, notebookName string) []fabric.Parameter {
	fav, ok := customer.FavoriteFor(notebookName)
	if !ok || len(fav.Parameters) == 0 {
		return params
	}
	pinned := make(map[string]bool, len(fav.Parameters))
	for _, p := range fav.Parameters {
		pinned[p] = true
	}
	out := make([]fabric.Parameter, 0, len(fav.Parameters))
	for _, p := range params {
		if pinned[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

// lakehouseItemType is the Fabric item type used to filter ListItemsByType
// when hunting for a notebook's default lakehouse by GUID.
const lakehouseItemType = "Lakehouse"

// resolveLakehouseOverride inspects a notebook's .ipynb metadata and, only
// when it finds the broken binding pattern (a lakehouse GUID pinned with no
// workspace id), resolves the lakehouse's real home workspace and returns a
// per-run DefaultLakehouse override. Returns (nil, nil) for a complete
// binding, a notebook with no lakehouse, or unparseable metadata — in every
// such case futils sends no override and the notebook's own binding stands.
func resolveLakehouseOverride(client APIClient, token string, ipynb []byte, runWorkspaceID string) (*fabric.DefaultLakehouse, error) {
	binding, err := fabric.ParseLakehouseBinding(ipynb)
	if err != nil || !binding.NeedsWorkspaceResolution() {
		return nil, nil //nolint:nilerr // unparseable metadata → leave binding alone
	}

	spinner := ui.NewSpinner("Resolving notebook's default lakehouse...")
	spinner.Start()
	lh, err := resolveDefaultLakehouse(client, token, binding.LakehouseID, runWorkspaceID)
	spinner.Stop()
	return lh, err
}

// resolveDefaultLakehouse finds which workspace a lakehouse GUID lives in.
// A lakehouse item id is globally unique, so at most one workspace matches.
// The run workspace is checked first — the overwhelmingly common case, and a
// single API call — before falling back to scanning every accessible
// workspace. A clear error (rather than a guessed workspace) is returned when
// the lakehouse can't be found anywhere the user has access.
func resolveDefaultLakehouse(client APIClient, token, lakehouseID, runWorkspaceID string) (*fabric.DefaultLakehouse, error) {
	if lh, ok := findLakehouseInWorkspace(client, token, runWorkspaceID, lakehouseID); ok {
		return lh, nil
	}

	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return nil, fmt.Errorf("list workspaces to resolve default lakehouse: %w", err)
	}
	for _, ws := range workspaces {
		if ws.ID == runWorkspaceID {
			continue // already checked first
		}
		if lh, ok := findLakehouseInWorkspace(client, token, ws.ID, lakehouseID); ok {
			return lh, nil
		}
	}
	return nil, fmt.Errorf(
		"notebook pins default lakehouse %s but its binding has no workspace, "+
			"and that lakehouse wasn't found in any workspace you can access — "+
			"fix the notebook's default lakehouse, or check your permissions",
		lakehouseID)
}

// findLakehouseInWorkspace looks for a lakehouse with the given GUID in one
// workspace. A listing error is treated as "not here" so a single
// inaccessible workspace during a scan doesn't abort the whole resolution.
func findLakehouseInWorkspace(client APIClient, token, workspaceID, lakehouseID string) (*fabric.DefaultLakehouse, bool) {
	items, err := client.ListItemsByType(token, workspaceID, lakehouseItemType)
	if err != nil {
		return nil, false
	}
	for _, it := range items {
		if it.ID == lakehouseID {
			return &fabric.DefaultLakehouse{Name: it.DisplayName, ID: it.ID, WorkspaceID: workspaceID}, true
		}
	}
	return nil, false
}

// describeOverrides produces a short human-readable summary of what's
// actually being sent to Fabric. Used only in the confirmation screen.
func describeOverrides(overrides []fabric.JobInput) string {
	if len(overrides) == 0 {
		return "(none — notebook uses its own defaults)"
	}
	parts := make([]string, len(overrides))
	for i, o := range overrides {
		parts[i] = fmt.Sprintf("%s=%v", o.Name, o.Value)
	}
	return strings.Join(parts, ", ")
}
