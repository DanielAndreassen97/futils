package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Swap hooks for testing. Tests replace these to drive the flow
// deterministically without spinning up bubbletea.
var (
	moveFilterPicker = ui.FilterMenu
	moveNumberPicker = ui.NumberMenu
	movePromptInput  = defaultPromptInput
	moveConfirm      = ui.Confirm
)

// Collision-resolution sentinel values used by both the menu and
// the dispatcher.
const (
	collisionOverwrite = "__overwrite"
	collisionRename    = "__rename"
	collisionCancel    = "__cancel"
	rebindSkip         = "__skip"

	// labelColW is the rebind picker's label column width: unselected rows
	// fit their label into this many columns so the workspace names align,
	// while the selected row pads to it but isn't truncated (revealing the
	// full name under the cursor).
	labelColW = 40
)

// Supported item types in v1. Items outside this set are filtered
// out of the source picker.
var moveSupportedTypes = map[string]bool{
	"Report":        true,
	"SemanticModel": true,
	"Notebook":      true,
}

// Move is the top-level entry point for the `move` subcommand.
func Move(configPath string) error {
	return MoveWithAPI(configPath, DefaultAPI)
}

// MoveWithAPI is the testable entry point. Tests swap DefaultAPI
// for a fake before calling.
func MoveWithAPI(configPath string, client APIClient) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Customers) == 0 {
		fmt.Println("No customers configured. Add a customer first.")
		return nil
	}

	customerName, _, err := selectCustomer(cfg)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(infoStyle.Render("Authenticating..."))
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println(infoStyle.Render("Authenticated."))
	fmt.Println()

	spinner := ui.NewSpinner("Loading workspaces...")
	spinner.Start()
	workspaces, err := client.ListWorkspaces(token)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	if len(workspaces) < 2 {
		return fmt.Errorf("only one workspace available — nothing to move to")
	}

	srcWS, err := pickWorkspace("Select source workspace", workspaces, "")
	if err != nil {
		return err
	}

	srcItem, err := pickSourceItem(client, token, srcWS)
	if err != nil {
		return err
	}

	dstWS, err := pickWorkspace("Select destination workspace", workspaces, srcWS.ID)
	if err != nil {
		return err
	}

	defSpinner := ui.NewSpinner("Fetching item definition...")
	defSpinner.Start()
	def, err := client.GetItemDefinition(token, srcWS.ID, srcItem.ID, formatForType(srcItem.Type))
	defSpinner.Stop()
	if err != nil {
		return fmt.Errorf("can't read item %q in %s: %w", srcItem.DisplayName, srcWS.DisplayName, err)
	}

	targetName, collisionAction, err := resolveCollision(client, token, dstWS, srcItem)
	if err != nil {
		return err
	}
	if collisionAction == collisionCancel {
		fmt.Println("Cancelled.")
		return nil
	}

	rebindDatasetID := ""
	rebindLabel := ""
	if srcItem.Type == "Report" {
		rebindDatasetID, rebindLabel, err = pickRebindTarget(client, token, dstWS, workspaces)
		if err != nil {
			return err
		}
	}

	printMoveSummary(customerName, srcWS, srcItem, dstWS, targetName, collisionAction, rebindLabel)
	confirmed, err := moveConfirm("Start move?")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	return executeMove(client, token, srcWS, srcItem, dstWS, targetName, collisionAction, def, rebindDatasetID, rebindLabel)
}

// defaultPromptInput shows a single-field huh form for free text.
// Used for the rename branch of collision resolution.
func defaultPromptInput(title, placeholder string) (string, error) {
	var v string
	input := huh.NewInput().Title(title).Placeholder(placeholder).Value(&v)
	if err := runFormStep(input); err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// formatForType returns the ?format= value to pass to
// GetItemDefinition for a given Fabric item type. Only notebooks
// use a non-empty format.
func formatForType(itemType string) string {
	if itemType == "Notebook" {
		return "ipynb"
	}
	return ""
}

// pickWorkspace renders the searchable workspace picker, excluding
// excludeID if non-empty (used for the destination picker so the
// source can't be selected again).
func pickWorkspace(prompt string, workspaces []fabric.Workspace, excludeID string) (fabric.Workspace, error) {
	options := make([]ui.FilterOption, 0, len(workspaces))
	byValue := make(map[string]fabric.Workspace, len(workspaces))
	for _, w := range workspaces {
		if w.ID == excludeID {
			continue
		}
		options = append(options, ui.FilterOption{Label: w.DisplayName, Value: w.ID})
		byValue[w.ID] = w
	}
	if len(options) == 0 {
		return fabric.Workspace{}, fmt.Errorf("no workspaces available for %s", prompt)
	}
	chosen, err := moveFilterPicker(prompt, options, ui.DefaultFilterRowRenderer)
	if err != nil {
		return fabric.Workspace{}, err
	}
	return byValue[chosen], nil
}

// pickSourceItem lists items in the source workspace, filters to
// the supported subset, and shows the searchable picker with a
// colorized type column.
func pickSourceItem(client APIClient, token string, srcWS fabric.Workspace) (fabric.Item, error) {
	spinner := ui.NewSpinner("Listing items...")
	spinner.Start()
	items, err := client.ListItems(token, srcWS.ID)
	spinner.Stop()
	if err != nil {
		return fabric.Item{}, fmt.Errorf("list items: %w", err)
	}
	var supported []fabric.Item
	for _, it := range items {
		if moveSupportedTypes[it.Type] {
			supported = append(supported, it)
		}
	}
	if len(supported) == 0 {
		return fabric.Item{}, fmt.Errorf("no reports, semantic models, or notebooks in %s", srcWS.DisplayName)
	}

	options := make([]ui.FilterOption, len(supported))
	byID := make(map[string]fabric.Item, len(supported))
	for i, it := range supported {
		options[i] = ui.FilterOption{Label: it.DisplayName, Value: it.ID, Meta: it.Type}
		byID[it.ID] = it
	}

	chosen, err := moveFilterPicker("Select item to move", options, itemRowRenderer)
	if err != nil {
		return fabric.Item{}, err
	}
	return byID[chosen], nil
}

// itemRowRenderer renders one row of the item picker as
// "<name>  <type>" with the type column colorized via
// ui.ItemTypeColor. Cursor row inverts the entire row regardless
// of type color so the highlight is always visible.
func itemRowRenderer(opt ui.FilterOption, selected bool) string {
	itemType, _ := opt.Meta.(string)
	if selected {
		// Selection wins over type coloring: a uniform accent-on-
		// black row so the cursor is unambiguous.
		row := fmt.Sprintf("%-40s  %s", opt.Label, itemType)
		return lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true).Render(row)
	}
	typeColored := lipgloss.NewStyle().Foreground(ui.ItemTypeColor(itemType)).Render(itemType)
	return fmt.Sprintf("%-40s  %s", opt.Label, typeColored)
}

// resolveCollision returns (targetName, action). action is one of
// collisionOverwrite / collisionRename / collisionCancel, or "" if
// no collision was detected. targetName is the display name to use
// in the destination (same as source if no collision, or the new
// name in the rename branch). For overwrite, targetName also
// matches the existing item's name and action carries the existing
// item's ID via a small side channel below.
func resolveCollision(client APIClient, token string, dstWS fabric.Workspace, srcItem fabric.Item) (string, string, error) {
	const maxRetries = 5
	name := srcItem.DisplayName
	for attempt := 0; attempt < maxRetries; attempt++ {
		existing, err := findItemByName(client, token, dstWS.ID, srcItem.Type, name)
		if err != nil {
			return "", "", err
		}
		if existing.ID == "" {
			return name, "", nil // no collision
		}

		action, err := moveNumberPicker(
			fmt.Sprintf("%q already exists in %s. What now?", name, dstWS.DisplayName),
			[]ui.MenuOption{
				{Label: "Overwrite the existing item", Value: collisionOverwrite},
				{Label: "Create with a new name", Value: collisionRename},
				{Label: "Cancel", Value: collisionCancel},
			})
		if err != nil {
			return "", "", err
		}
		switch action {
		case collisionOverwrite:
			// Stash the existing ID so executeMove can call
			// UpdateItemDefinition on it. We encode the ID after a
			// pipe in the action string — ugly but avoids a second
			// return value just for this case.
			return name, collisionOverwrite + "|" + existing.ID, nil
		case collisionRename:
			newName, err := movePromptInput("New display name", name+"-copy")
			if err != nil {
				return "", "", err
			}
			if newName == "" {
				return "", "", fmt.Errorf("rename cancelled (empty name)")
			}
			name = newName
			continue
		case collisionCancel:
			return "", collisionCancel, nil
		}
	}
	return "", "", fmt.Errorf("too many rename attempts — cancelling")
}

// findItemByName returns the existing item with the given name and
// type in the destination, or an empty Item if not found.
func findItemByName(client APIClient, token, workspaceID, itemType, name string) (fabric.Item, error) {
	items, err := client.ListItemsByType(token, workspaceID, itemType)
	if err != nil {
		return fabric.Item{}, fmt.Errorf("list dest items: %w", err)
	}
	for _, it := range items {
		if it.DisplayName == name {
			return it, nil
		}
	}
	return fabric.Item{}, nil
}

// pickRebindTarget shows the semantic-model picker for Reports.
// Lists semantic models across ALL workspaces the user can see —
// the Power BI Rebind endpoint accepts cross-workspace datasetIds, and
// in practice models often live in shared workspaces separate
// from where Reports get developed. The destination workspace is listed
// first so its models surface at the top of the picker: after a move you
// almost always rebind to a model that lives in the destination.
//
// Returns (datasetID, label, err). label is "<model> (<workspace>)"
// so the post-move summary shows where the binding pointed.
// ("", "", nil) means user picked Skip.
func pickRebindTarget(client APIClient, token string, dstWS fabric.Workspace, workspaces []fabric.Workspace) (string, string, error) {
	spinner := ui.NewSpinner("Listing semantic models across workspaces...")
	spinner.Start()
	type modelRow struct {
		model  fabric.Item
		wsName string
	}

	// Destination first, then the rest — so the destination's models sort to
	// the top. dstWS is always one of workspaces, so skip its duplicate.
	ordered := make([]fabric.Workspace, 0, len(workspaces))
	ordered = append(ordered, dstWS)
	for _, ws := range workspaces {
		if ws.ID != dstWS.ID {
			ordered = append(ordered, ws)
		}
	}

	var all []modelRow
	failed := 0
	for _, ws := range ordered {
		items, err := client.ListItemsByType(token, ws.ID, "SemanticModel")
		if err != nil {
			// Skip workspaces the user can't read items from —
			// RBAC means some are visible-but-unenumerable. We count
			// these so the picker title can surface "couldn't list N
			// workspace(s)" — without that hint, a transient 5xx
			// against every workspace would silently show an empty
			// picker and the user would assume no models exist.
			failed++
			continue
		}
		for _, m := range items {
			all = append(all, modelRow{model: m, wsName: ws.DisplayName})
		}
	}
	spinner.Stop()

	title := "Rebind to which semantic model?"
	if failed > 0 {
		title += fmt.Sprintf(" (couldn't list %d workspace(s) — may be RBAC or transient errors)", failed)
	}

	options := []ui.FilterOption{
		{Label: "⋯ Skip (keep current binding)", Value: rebindSkip},
	}
	byValue := make(map[string]modelRow, len(all))
	for _, m := range all {
		options = append(options, ui.FilterOption{
			Label: m.model.DisplayName,
			Value: m.model.ID,
			Meta:  m.wsName,
		})
		byValue[m.model.ID] = m
	}

	chosen, err := moveFilterPicker(title, options, rebindRowRenderer)
	if err != nil {
		return "", "", err
	}
	if chosen == rebindSkip {
		return "", "", nil
	}
	row := byValue[chosen]
	return chosen, fmt.Sprintf("%s (%s)", row.model.DisplayName, row.wsName), nil
}

// rebindRowRenderer renders one row of the rebind picker as
// "<model name>   <workspace name>" with the workspace name dimmed.
// The Skip row has no workspace metadata and renders alone.
// Selected rows render in a uniform accent color regardless of
// dimming, so the cursor is always visible.
func rebindRowRenderer(opt ui.FilterOption, selected bool) string {
	wsName, _ := opt.Meta.(string)
	if selected {
		if wsName == "" {
			return lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true).Render(opt.Label)
		}
		row := fmt.Sprintf("%-*s  %s", labelColW, opt.Label, wsName)
		return lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true).Render(row)
	}
	if wsName == "" {
		return opt.Label
	}
	return fmt.Sprintf("%s  %s", ui.FitWidth(opt.Label, labelColW),
		lipgloss.NewStyle().Foreground(ui.DimColor).Render(wsName))
}

// printMoveSummary writes the summary box (same look as run.go's
// run-summary block). collisionAction may carry the "|<id>" suffix
// from resolveCollision; strip it for display.
func printMoveSummary(customerName string, srcWS fabric.Workspace, srcItem fabric.Item, dstWS fabric.Workspace, targetName, collisionAction, rebindLabel string) {
	fmt.Println()
	fmt.Println(infoStyle.Render("Move summary"))
	fmt.Printf("  Customer:         %s\n", customerName)
	fmt.Printf("  Source:           %s  →  %s (%s)\n", srcWS.DisplayName, srcItem.DisplayName, srcItem.Type)
	fmt.Printf("  Destination:      %s\n", dstWS.DisplayName)
	if action := strings.SplitN(collisionAction, "|", 2)[0]; action == collisionOverwrite {
		fmt.Printf("  On collision:     Overwrite existing %q\n", targetName)
	} else if targetName != srcItem.DisplayName {
		fmt.Printf("  Rename to:        %s\n", targetName)
	}
	if rebindLabel != "" {
		fmt.Printf("  Rebind dataset:   %s\n", rebindLabel)
	}
	fmt.Println()
}

// executeMove performs the create-or-update + optional rebind,
// wrapped in a single spinner. Returns nil on full or partial
// success — partial success is a status (rendered via
// warningStyle), not a Go error.
func executeMove(client APIClient, token string, srcWS fabric.Workspace, srcItem fabric.Item, dstWS fabric.Workspace, targetName, collisionAction string, def *fabric.Definition, rebindDatasetID, rebindLabel string) error {
	startTime := time.Now()
	spinner := ui.NewSpinner("Moving item...")
	spinner.Start()

	var newID string
	var moveErr, rebindErr error

	func() {
		defer spinner.Stop()

		action := strings.SplitN(collisionAction, "|", 2)
		if len(action) == 2 && action[0] == collisionOverwrite {
			existingID := action[1]
			if err := client.UpdateItemDefinition(token, dstWS.ID, existingID, def); err != nil {
				moveErr = fmt.Errorf("update item: %w (check that you have Member or higher on %s)", err, dstWS.DisplayName)
				return
			}
			newID = existingID
		} else {
			created, err := client.CreateItem(token, dstWS.ID, targetName, srcItem.Type, def, nil)
			if err != nil {
				moveErr = fmt.Errorf("create item: %w (check that you have Member or higher on %s)", err, dstWS.DisplayName)
				return
			}
			newID = created.ID
		}

		if srcItem.Type == "Report" && rebindDatasetID != "" {
			if err := client.RebindReport(token, dstWS.ID, newID, rebindDatasetID); err != nil {
				rebindErr = err
				return
			}
		}
	}()

	duration := time.Since(startTime).Round(time.Second)

	fmt.Println()
	switch {
	case moveErr != nil:
		fmt.Println(errorStyle.Render(fmt.Sprintf("Move failed (%s)\n  %v", duration, moveErr)))
	case rebindErr != nil:
		msg := fmt.Sprintf(
			"⚠ Item copied, rebind failed (%s)\n"+
				"  Item:    %s (%s) → %s\n"+
				"  Rebind:  failed — %v\n\n"+
				"  The report is in the destination workspace but still bound to its\n"+
				"  original dataset. Rebind manually in Fabric, or re-run \"Move item\".",
			duration, targetName, srcItem.Type, dstWS.DisplayName, rebindErr)
		fmt.Println(warningStyle.Render(msg))
	default:
		msg := fmt.Sprintf("Item copied successfully (%s)\n  %s (%s) → %s", duration, targetName, srcItem.Type, dstWS.DisplayName)
		if srcItem.Type == "Report" && rebindDatasetID != "" {
			msg += "\n  Rebound to: " + rebindLabel
		}
		fmt.Println(successStyle.Render(msg))
	}
	return nil
}
