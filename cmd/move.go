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
	moveMultiPicker  = ui.MultiSelectRichFiltered
	moveNumberPicker = ui.NumberMenu
	movePromptInput  = defaultPromptInput
	moveConfirm      = ui.Confirm
)

// Collision-resolution sentinel values used by both the menu and
// the dispatcher.
const (
	collisionOverwrite = "__overwrite"
	collisionRename    = "__rename"
	collisionSkip      = "__skip_item"
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

	idx, err := loadWorkspaceIndex(client, token)
	if err != nil {
		return err
	}
	if len(idx.Workspaces) < 2 {
		return fmt.Errorf("only one workspace available — nothing to move to")
	}

	srcWS, err := pickWorkspace("Select source workspace", idx, "")
	if err != nil {
		return err
	}

	srcItems, err := pickSourceItems(client, token, srcWS)
	if err != nil {
		return err
	}
	if len(srcItems) == 0 {
		fmt.Println("No items selected.")
		return nil
	}

	return moveItemsFrom(client, token, srcWS, srcItems, idx, customerName)
}

// moveItemFrom moves a single item the workspace item browser is already
// looking at. It is the one-element case of moveItemsFrom, so the browser and
// the top-level Move entry share collision handling and the report rebind.
func moveItemFrom(client APIClient, token string, srcWS fabric.Workspace, srcItem fabric.Item, idx workspaceIndex, customerName string) error {
	return moveItemsFrom(client, token, srcWS, []fabric.Item{srcItem}, idx, customerName)
}

// movePlan is one item's resolved move: what to write, under which name, and
// whether it replaces an existing item or is created fresh. Rebind fields are
// only set for reports whose user picked a model.
type movePlan struct {
	item        fabric.Item
	def         *fabric.Definition
	targetName  string
	overwriteID string // non-empty: update this existing item instead of creating
	rebindID    string
	rebindLabel string
}

// moveItemsFrom is everything the move flow does once the source workspace and
// items are known: pick one destination, then per item read the definition,
// resolve a name collision and (for reports) pick a rebind target. Every
// question is asked before the summary, so the confirm covers the whole batch
// and the execution runs unattended.
//
// Cancel at a collision prompt aborts the batch; Skip drops that item only.
func moveItemsFrom(client APIClient, token string, srcWS fabric.Workspace, srcItems []fabric.Item, idx workspaceIndex, customerName string) error {
	dstWS, err := pickWorkspace("Select destination workspace", idx, srcWS.ID)
	if err != nil {
		return err
	}

	plans := make([]movePlan, 0, len(srcItems))
	for _, srcItem := range srcItems {
		defSpinner := ui.NewSpinner(fmt.Sprintf("Fetching definition of %s...", srcItem.DisplayName))
		defSpinner.Start()
		def, err := client.GetItemDefinition(token, srcWS.ID, srcItem.ID, formatForType(srcItem.Type))
		defSpinner.Stop()
		if err != nil {
			return fmt.Errorf("can't read item %q in %s: %w", srcItem.DisplayName, srcWS.DisplayName, err)
		}

		outcome, err := resolveCollision(client, token, dstWS, srcItem)
		if err != nil {
			return err
		}
		switch outcome.action {
		case collisionCancel:
			fmt.Println("Cancelled.")
			return nil
		case collisionSkip:
			fmt.Println(warningStyle.Render(fmt.Sprintf("Skipping %s.", srcItem.DisplayName)))
			continue
		}

		plans = append(plans, movePlan{item: srcItem, def: def, targetName: outcome.name, overwriteID: outcome.overwriteID})
	}
	if len(plans) == 0 {
		fmt.Println("Nothing left to move.")
		return nil
	}
	if err := planRebinds(client, token, dstWS, idx.Workspaces, plans); err != nil {
		return err
	}

	printMoveSummary(customerName, srcWS, dstWS, plans)
	confirmed, err := moveConfirm("Start move?")
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Println("Cancelled.")
		return nil
	}

	executeMoves(client, token, dstWS, plans)
	return nil
}

// Rebind-plan menu values.
const (
	rebindPlanContinue = "__rebind_continue"
	rebindPlanChange   = "__rebind_change"
)

// planRebinds fills in the rebind target for every report in the batch. One
// picker sets a default for all of them; with several reports a review screen
// then lists report → model and lets the user re-point any subset, repeatedly,
// until they continue. Thirteen reports going to the same model is the common
// case, and thirteen identical pickers in a row was the complaint.
func planRebinds(client APIClient, token string, dstWS fabric.Workspace, workspaces []fabric.Workspace, plans []movePlan) error {
	var reports []int
	for i, p := range plans {
		if p.item.Type == "Report" {
			reports = append(reports, i)
		}
	}
	if len(reports) == 0 {
		return nil
	}

	models := listRebindModels(client, token, dstWS, workspaces)

	title := "Rebind to which semantic model?"
	if len(reports) > 1 {
		title = fmt.Sprintf("Rebind all %d reports to which semantic model?", len(reports))
	}
	id, label, err := pickRebindModel(title, models)
	if err != nil {
		return err
	}
	for _, i := range reports {
		plans[i].rebindID, plans[i].rebindLabel = id, label
	}
	if len(reports) == 1 {
		return nil
	}

	for {
		printRebindPlan(plans, reports)
		choice, err := moveNumberPicker("Rebind plan", []ui.MenuOption{
			{Label: "Continue with this plan", Value: rebindPlanContinue},
			{Label: "Change model for some reports", Value: rebindPlanChange, Description: "Pick one or more reports, then a model for them"},
		})
		if err != nil {
			return err
		}
		if choice == rebindPlanContinue {
			return nil
		}

		rows := make([]ui.CheckItem, len(reports))
		for k, i := range reports {
			rows[k] = ui.CheckItem{Label: ui.FitWidth(plans[i].item.DisplayName, labelColW) + "  → " + rebindLabelOrKeep(plans[i].rebindLabel)}
		}
		checked, err := moveMultiPicker("Which reports get a different model?", rows)
		if err != nil {
			return err
		}
		if len(checked) == 0 {
			continue
		}
		id, label, err := pickRebindModel(fmt.Sprintf("Rebind %d report(s) to which semantic model?", len(checked)), models)
		if err != nil {
			return err
		}
		for _, k := range checked {
			i := reports[k]
			plans[i].rebindID, plans[i].rebindLabel = id, label
		}
	}
}

// rebindLabelOrKeep is the wording for an empty rebind target.
func rebindLabelOrKeep(label string) string {
	if label == "" {
		return "keep current binding"
	}
	return label
}

// printRebindPlan lists each report and the model it will be bound to.
func printRebindPlan(plans []movePlan, reports []int) {
	fmt.Println()
	fmt.Println(infoStyle.Render("Rebind plan"))
	for _, i := range reports {
		p := plans[i]
		fmt.Printf("  %s  → %s\n", ui.FitWidth(p.item.DisplayName, labelColW),
			lipgloss.NewStyle().Foreground(ui.DimColor).Render(rebindLabelOrKeep(p.rebindLabel)))
	}
	fmt.Println()
}

// moveWriteError wraps a failed write to the destination workspace. The
// permission hint is only added when the failure actually looks like one:
// appending it to every error sent a Fabric conversion failure — which names its
// own cause perfectly well — off to check access rights that were already fine.
func moveWriteError(what string, err error, dstName string) error {
	if looksLikePermissionDenied(err) {
		return fmt.Errorf("%s: %w (check that you have Member or higher on %s)", what, err, dstName)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// looksLikePermissionDenied recognises the shapes Fabric refuses a write in.
// Matching on the message is crude, but the error has already been flattened to
// a string by the time it reaches here, and the alternative — threading status
// codes through every client method — buys nothing else.
func looksLikePermissionDenied(err error) bool {
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"401", "403", "unauthorized", "forbidden", "insufficientprivileges", "permission"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
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

// pickWorkspace shows the same grouped view as the Manage workspaces screen:
// role sections, licence tier per row, filterable. A flat list of forty names
// hides the one thing that decides whether a move will work — whether you have
// more than read access where it is going.
func pickWorkspace(prompt string, idx workspaceIndex, excludeID string) (fabric.Workspace, error) {
	eligible := make([]fabric.Workspace, 0, len(idx.Workspaces))
	for _, w := range idx.Workspaces {
		if w.ID != excludeID {
			eligible = append(eligible, w)
		}
	}
	if len(eligible) == 0 {
		return fabric.Workspace{}, fmt.Errorf("no workspaces available for %s", prompt)
	}

	options := groupWorkspacesByRole(eligible, idx.RoleOf, idx.Capacities)
	chosen, err := moveFilterPicker(prompt, options, renderWorkspaceRow)
	if err != nil {
		return fabric.Workspace{}, err
	}
	ws, _ := workspaceByID(eligible, chosen)
	return ws, nil
}

// pickSourceItems lists items in the source workspace, filters to the supported
// subset, and shows a searchable checkbox list with each row in its type's
// Fabric colour. Returns the checked items in list order; an empty slice means
// the user confirmed with nothing checked.
func pickSourceItems(client APIClient, token string, srcWS fabric.Workspace) ([]fabric.Item, error) {
	spinner := ui.NewSpinner("Listing items...")
	spinner.Start()
	items, err := client.ListItems(token, srcWS.ID)
	spinner.Stop()
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	var supported []fabric.Item
	for _, it := range items {
		if moveSupportedTypes[it.Type] {
			supported = append(supported, it)
		}
	}
	if len(supported) == 0 {
		return nil, fmt.Errorf("no reports, semantic models, or notebooks in %s", srcWS.DisplayName)
	}

	rows := make([]ui.CheckItem, len(supported))
	for i, it := range supported {
		rows[i] = ui.CheckItem{
			Label: ui.FitWidth(it.DisplayName, labelColW) + "  " + it.Type,
			Style: lipgloss.NewStyle().Foreground(ui.ItemTypeColor(it.Type)),
		}
	}

	checked, err := moveMultiPicker("Select items to move", rows)
	if err != nil {
		return nil, err
	}
	chosen := make([]fabric.Item, 0, len(checked))
	for _, i := range checked {
		chosen = append(chosen, supported[i])
	}
	return chosen, nil
}

// collisionOutcome is what resolveCollision decided for one item. name is the
// display name to write in the destination; overwriteID is set when that name
// belongs to an existing item the user chose to overwrite. action is only
// collisionSkip (drop this item) or collisionCancel (abort the batch); a
// resolved move, overwrite or not, leaves it empty.
type collisionOutcome struct {
	name        string
	overwriteID string
	action      string
}

// resolveCollision checks the destination for an item of the same type and
// name and, if there is one, asks what to do. Rename loops back to re-check
// the new name, so a rename onto another existing item is caught too.
func resolveCollision(client APIClient, token string, dstWS fabric.Workspace, srcItem fabric.Item) (collisionOutcome, error) {
	const maxRetries = 5
	name := srcItem.DisplayName
	for attempt := 0; attempt < maxRetries; attempt++ {
		existing, err := findItemByName(client, token, dstWS.ID, srcItem.Type, name)
		if err != nil {
			return collisionOutcome{}, err
		}
		if existing.ID == "" {
			return collisionOutcome{name: name}, nil
		}

		action, err := moveNumberPicker(
			fmt.Sprintf("%q already exists in %s. What now?", name, dstWS.DisplayName),
			[]ui.MenuOption{
				{Label: "Overwrite the existing item", Value: collisionOverwrite},
				{Label: "Create with a new name", Value: collisionRename},
				{Label: "Skip this item", Value: collisionSkip, Description: "Leave it out and move the rest"},
				{Label: "Cancel", Value: collisionCancel, Description: "Abort the whole move"},
			})
		if err != nil {
			return collisionOutcome{}, err
		}
		switch action {
		case collisionOverwrite:
			return collisionOutcome{name: name, overwriteID: existing.ID}, nil
		case collisionRename:
			newName, err := movePromptInput("New display name", name+"-copy")
			if err != nil {
				return collisionOutcome{}, err
			}
			if newName == "" {
				return collisionOutcome{}, fmt.Errorf("rename cancelled (empty name)")
			}
			name = newName
			continue
		case collisionSkip:
			return collisionOutcome{action: collisionSkip}, nil
		case collisionCancel:
			return collisionOutcome{action: collisionCancel}, nil
		}
	}
	return collisionOutcome{}, fmt.Errorf("too many rename attempts — cancelling")
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

// rebindModel is one semantic model the rebind picker offers, with the
// workspace it lives in for the dimmed second column and the summary label.
type rebindModel struct {
	model  fabric.Item
	wsName string
}

// rebindModels is the picker's model list plus how many workspaces could not
// be listed, so the title can say so instead of showing a silently empty list.
type rebindModels struct {
	rows   []rebindModel
	failed int
}

// listRebindModels gathers semantic models across ALL workspaces the user can
// see — the Power BI Rebind endpoint accepts cross-workspace datasetIds, and in
// practice models often live in shared workspaces separate from where Reports
// get developed. The destination workspace is listed first so its models
// surface at the top of the picker: after a move you almost always rebind to a
// model that lives in the destination.
//
// Listed once per batch, not once per picker: the list is a request per
// workspace, and the review screen may open the picker several times.
func listRebindModels(client APIClient, token string, dstWS fabric.Workspace, workspaces []fabric.Workspace) rebindModels {
	spinner := ui.NewSpinner("Listing semantic models across workspaces...")
	spinner.Start()
	defer spinner.Stop()

	// Destination first, then the rest — so the destination's models sort to
	// the top. dstWS is always one of workspaces, so skip its duplicate.
	ordered := make([]fabric.Workspace, 0, len(workspaces))
	ordered = append(ordered, dstWS)
	for _, ws := range workspaces {
		if ws.ID != dstWS.ID {
			ordered = append(ordered, ws)
		}
	}

	var out rebindModels
	for _, ws := range ordered {
		items, err := client.ListItemsByType(token, ws.ID, "SemanticModel")
		if err != nil {
			// Skip workspaces the user can't read items from — RBAC means some
			// are visible-but-unenumerable. Counted so the picker title can say
			// "couldn't list N workspace(s)"; without that hint a transient 5xx
			// against every workspace would look like no models exist.
			out.failed++
			continue
		}
		for _, m := range items {
			out.rows = append(out.rows, rebindModel{model: m, wsName: ws.DisplayName})
		}
	}
	return out
}

// pickRebindModel shows the semantic-model picker with a Skip row on top.
// Returns (datasetID, label, err). label is "<model> (<workspace>)" so the
// summary shows where the binding pointed. ("", "", nil) means Skip.
func pickRebindModel(title string, models rebindModels) (string, string, error) {
	if models.failed > 0 {
		title += fmt.Sprintf(" (couldn't list %d workspace(s) — may be RBAC or transient errors)", models.failed)
	}

	options := []ui.FilterOption{
		{Label: "⋯ Skip (keep current binding)", Value: rebindSkip},
	}
	byValue := make(map[string]rebindModel, len(models.rows))
	for _, m := range models.rows {
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
	lead := ui.CursorPointer(selected)
	if wsName == "" {
		return lead + ui.CursorLabel(opt.Label, selected)
	}
	if selected {
		return lead + ui.CursorLabel(ui.FitWidth(opt.Label, labelColW)+"  "+wsName, true)
	}
	return lead + ui.FitWidth(opt.Label, labelColW) + "  " +
		lipgloss.NewStyle().Foreground(ui.DimColor).Render(wsName)
}

// printMoveSummary writes the summary box (same look as run.go's run-summary
// block): the shared source and destination once, then one line per item with
// its rename, overwrite or rebind decision where there is one.
func printMoveSummary(customerName string, srcWS, dstWS fabric.Workspace, plans []movePlan) {
	fmt.Println()
	fmt.Println(infoStyle.Render("Move summary"))
	fmt.Printf("  Customer:         %s\n", customerName)
	fmt.Printf("  Source:           %s\n", srcWS.DisplayName)
	fmt.Printf("  Destination:      %s\n", dstWS.DisplayName)
	fmt.Printf("  Items:            %d\n", len(plans))
	for _, p := range plans {
		fmt.Printf("    %s  %s\n", ui.FitWidth(p.item.DisplayName, labelColW),
			lipgloss.NewStyle().Foreground(ui.ItemTypeColor(p.item.Type)).Render(p.item.Type))
		switch {
		case p.overwriteID != "":
			fmt.Printf("      overwrites existing %q\n", p.targetName)
		case p.targetName != p.item.DisplayName:
			fmt.Printf("      renamed to %s\n", p.targetName)
		}
		if p.rebindLabel != "" {
			fmt.Printf("      rebind to %s\n", p.rebindLabel)
		}
	}
	fmt.Println()
}

// moveOutcome is one executed plan's result. A failed rebind after a successful
// copy is a warning, not a failure: the item exists in the destination.
type moveOutcome struct {
	moveErr   error
	rebindErr error
}

// executeMoves runs every plan in order and prints one status block per item.
// A failure does not stop the rest: each item is an independent write, and the
// user confirmed the whole list. With more than one item a tally closes the run.
func executeMoves(client APIClient, token string, dstWS fabric.Workspace, plans []movePlan) {
	copied, failed, warned := 0, 0, 0
	for _, p := range plans {
		out := executeMove(client, token, dstWS, p)
		switch {
		case out.moveErr != nil:
			failed++
		case out.rebindErr != nil:
			warned++
		default:
			copied++
		}
	}
	if len(plans) < 2 {
		return
	}
	fmt.Println()
	tally := fmt.Sprintf("%d of %d copied", copied+warned, len(plans))
	if warned > 0 {
		tally += fmt.Sprintf(", %d rebind warning(s)", warned)
	}
	if failed > 0 {
		tally += fmt.Sprintf(", %d failed", failed)
		fmt.Println(warningStyle.Render(tally))
		return
	}
	fmt.Println(successStyle.Render(tally))
}

// executeMove performs one plan's create-or-update plus optional rebind under a
// single spinner and prints its result. Partial success (copied, rebind failed)
// is rendered as a warning, not returned as an error.
func executeMove(client APIClient, token string, dstWS fabric.Workspace, p movePlan) moveOutcome {
	startTime := time.Now()
	spinner := ui.NewSpinner(fmt.Sprintf("Moving %s...", p.item.DisplayName))
	spinner.Start()

	var out moveOutcome
	func() {
		defer spinner.Stop()

		var newID string
		if p.overwriteID != "" {
			if err := client.UpdateItemDefinition(token, dstWS.ID, p.overwriteID, p.def); err != nil {
				out.moveErr = moveWriteError("update item", err, dstWS.DisplayName)
				return
			}
			newID = p.overwriteID
		} else {
			created, err := client.CreateItem(token, dstWS.ID, p.targetName, p.item.Type, p.def, nil, "")
			if err != nil {
				out.moveErr = moveWriteError("create item", err, dstWS.DisplayName)
				return
			}
			newID = created.ID
		}

		if p.rebindID != "" {
			if err := client.RebindReport(token, dstWS.ID, newID, p.rebindID); err != nil {
				out.rebindErr = err
			}
		}
	}()

	duration := time.Since(startTime).Round(time.Second)

	fmt.Println()
	switch {
	case out.moveErr != nil:
		fmt.Println(errorStyle.Render(fmt.Sprintf("Move failed (%s)\n  %s (%s)\n  %v", duration, p.item.DisplayName, p.item.Type, out.moveErr)))
	case out.rebindErr != nil:
		msg := fmt.Sprintf(
			"⚠ Item copied, rebind failed (%s)\n"+
				"  Item:    %s (%s) → %s\n"+
				"  Rebind:  failed — %v\n\n"+
				"  The report is in the destination workspace but still bound to its\n"+
				"  original dataset. Rebind manually in Fabric, or re-run \"Move item\".",
			duration, p.targetName, p.item.Type, dstWS.DisplayName, out.rebindErr)
		fmt.Println(warningStyle.Render(msg))
	default:
		msg := fmt.Sprintf("Item copied successfully (%s)\n  %s (%s) → %s", duration, p.targetName, p.item.Type, dstWS.DisplayName)
		if p.rebindID != "" {
			msg += "\n  Rebound to: " + p.rebindLabel
		}
		fmt.Println(successStyle.Render(msg))
	}
	return out
}
