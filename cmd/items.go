package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// Sentinel values for the item action menu. Prefixed so they cannot collide
// with a Fabric item ID.
const (
	itemActionRun    = "__item_run"
	itemActionMove   = "__item_move"
	itemActionRename = "__item_rename"
	itemActionDesc   = "__item_description"
	itemActionDelete = "__item_delete"
	itemActionBack   = "__item_back"
)

// itemTypeGroupOrder is the order type sections appear in, by how often you act
// on them rather than alphabetically. Types not listed here follow, sorted by
// name, so a type Microsoft ships next year still lands somewhere sensible.
var itemTypeGroupOrder = []string{
	"Notebook", "DataPipeline", "SemanticModel", "Report", "Lakehouse", "Warehouse",
}

// itemTypePalette is Fabric's own item colouring, lifted from Microsoft's
// published icon set rather than invented: a lakehouse is blue in the portal and
// a notebook is green, so they are blue and green here. The point is that a
// glance at this list feels like a glance at the workspace in the browser.
//
// The colours are used as section-bar backgrounds. Several of them — the report
// gold and the lakehouse blue especially — are too dark to read as foreground
// text on a dark terminal.
var itemTypePalette = map[string]struct{ bg, fg lipgloss.Color }{
	// Data Engineering and Data Factory — green.
	"Notebook":           {"#45913e", "#f0fdf4"},
	"DataPipeline":       {"#45913e", "#f0fdf4"},
	"Environment":        {"#45913e", "#f0fdf4"},
	"Dataflow":           {"#45913e", "#f0fdf4"},
	"SparkJobDefinition": {"#45913e", "#f0fdf4"},
	"MLModel":            {"#45913e", "#f0fdf4"},
	"MLExperiment":       {"#45913e", "#f0fdf4"},
	"VariableLibrary":    {"#45913e", "#f0fdf4"},
	"CopyJob":            {"#45913e", "#f0fdf4"},
	// Lakehouse and Warehouse — blue and cyan.
	"Lakehouse": {"#2661be", "#eef4fd"},
	"Warehouse": {"#20b6ef", "#06222c"},
	// Databases and Real-Time Intelligence — blue.
	"SQLDatabase":  {"#007fca", "#eaf6ff"},
	"SQLEndpoint":  {"#007fca", "#eaf6ff"},
	"Eventhouse":   {"#007fca", "#eaf6ff"},
	"Eventstream":  {"#007fca", "#eaf6ff"},
	"KQLDatabase":  {"#007fca", "#eaf6ff"},
	"KQLQueryset":  {"#007fca", "#eaf6ff"},
	"KQLDashboard": {"#007fca", "#eaf6ff"},
	// Power BI — purple for models, gold for reports.
	"SemanticModel":   {"#744fb5", "#f6f0fc"},
	"Report":          {"#bc7d00", "#1c1300"},
	"PaginatedReport": {"#bc7d00", "#1c1300"},
	"Dashboard":       {"#bc7d00", "#1c1300"},
}

var itemTypeFallbackColor = struct{ bg, fg lipgloss.Color }{"#3a4547", "#d7dfe0"}

// runnableItemLabels maps an item type to the label of the action that runs it.
// A type absent here cannot be run, and its entry is badged rather than hidden.
var runnableItemLabels = map[string]string{
	"Notebook":      "Run notebook",
	"DataPipeline":  "Run pipeline",
	"SemanticModel": "Refresh tables",
}

// itemTypeColor returns the section-bar colours for an item type.
func itemTypeColor(itemType string) (bg, fg lipgloss.Color) {
	if c, ok := itemTypePalette[itemType]; ok {
		return c.bg, c.fg
	}
	return itemTypeFallbackColor.bg, itemTypeFallbackColor.fg
}

// groupItemsByType turns a workspace's items into picker rows grouped under a
// coloured heading per type, sorted case-insensitively inside each group. Each
// row carries the whole item in Meta, since the action menu needs its type.
func groupItemsByType(items []fabric.Item) []ui.FilterOption {
	byType := map[string][]fabric.Item{}
	for _, it := range items {
		typ := it.Type
		if typ == "" {
			typ = "Unknown"
		}
		byType[typ] = append(byType[typ], it)
	}

	// Known types first in their curated order, then whatever is left by name.
	var order []string
	seen := map[string]bool{}
	for _, typ := range itemTypeGroupOrder {
		if len(byType[typ]) > 0 {
			order = append(order, typ)
			seen[typ] = true
		}
	}
	var rest []string
	for typ := range byType {
		if !seen[typ] {
			rest = append(rest, typ)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)

	var out []ui.FilterOption
	for _, typ := range order {
		group := byType[typ]
		sort.SliceStable(group, func(i, j int) bool {
			return strings.ToLower(group[i].DisplayName) < strings.ToLower(group[j].DisplayName)
		})
		out = append(out, ui.FilterOption{
			Label:    fmt.Sprintf("%s · %d", strings.ToUpper(typ), len(group)),
			IsHeader: true,
			Meta:     itemTypeHeader{Type: typ},
		})
		for _, it := range group {
			out = append(out, ui.FilterOption{Label: it.DisplayName, Value: it.ID, Meta: it})
		}
	}
	return out
}

// itemTypeHeader marks a type section so the row renderer knows which colour to
// paint without parsing the label back apart.
type itemTypeHeader struct{ Type string }

// itemActions builds the action menu for one item. Actions the type cannot
// support stay in the list, badged with the reason: hiding them would reshuffle
// the menu between items and move Delete under a different number each time.
func itemActions(item fabric.Item) []ui.MenuOption {
	runLabel, runnable := runnableItemLabels[item.Type]
	if !runnable {
		runLabel = "Run"
	}
	runOpt := ui.MenuOption{Value: itemActionRun, Label: runLabel}
	if !runnable {
		runOpt.Badge = "NOT RUNNABLE"
		runOpt.Description = item.Type + " items have nothing to run"
	}

	moveOpt := ui.MenuOption{Value: itemActionMove, Label: "Move to another workspace"}
	if !moveSupportedTypes[item.Type] {
		moveOpt.Badge = "NOT MOVABLE"
		moveOpt.Description = "Move supports Report, SemanticModel and Notebook"
	}

	delDesc := "Delete this item"
	if dataBearingItemTypes[item.Type] {
		delDesc = "Deletes the item AND its data — needs the word Yes typed"
	}

	return []ui.MenuOption{
		runOpt,
		moveOpt,
		{Value: itemActionRename, Label: "Rename", Description: "Change the display name"},
		{Value: itemActionDesc, Label: "Edit description", Description: "Replace the item description"},
		{Value: itemActionDelete, Label: "Delete", Description: delDesc},
		{Value: itemActionBack, Label: "Back"},
	}
}

// validateItemName enforces a non-empty name that does not collide with a
// sibling in the same workspace. Fabric's own naming rules vary per item type
// and are not published as a single set, so the API stays the authority — this
// only catches what can be checked without a round trip.
func validateItemName(name string, siblings []fabric.Item, current string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("item name cannot be empty")
	}
	if current != "" && name == current {
		return errors.New("the new name must differ from the current one")
	}
	for _, it := range siblings {
		if it.DisplayName == current {
			continue
		}
		if it.DisplayName == name {
			return fmt.Errorf("an item named %q already exists in this workspace", name)
		}
	}
	return nil
}

func validateItemDescription(desc string) error {
	if len([]rune(desc)) > fabric.MaxItemDescLen {
		return fmt.Errorf("description cannot be longer than %d characters", fabric.MaxItemDescLen)
	}
	return nil
}

// renderItemRefs lists the config references to an item, one indented line each.
// Returns the empty string for none so callers own the wording for that case.
func renderItemRefs(refs []config.ItemRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, ref := range refs {
		what := ref.Kind.String()
		if ref.Detail != "" {
			what += fmt.Sprintf(" (%s)", ref.Detail)
		}
		fmt.Fprintf(&b, "    %s  %s\n", ui.FitWidth(ref.Customer, 24), wsLabelStyle.Render(what))
	}
	return b.String()
}

// manageItem shows one item's details and runs a single action on it, then
// returns to the workspace screen so the next screen is drawn from fresh data.
func (s *workspaceSession) manageItem(ws fabric.Workspace, item fabric.Item, siblings []fabric.Item) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	refs := config.FindItemRefs(cfg, item.DisplayName)

	printItemPanel(ws, item, refs)

	choice, err := wsNumberPicker(item.DisplayName, itemActions(item))
	if err != nil {
		return err
	}

	switch choice {
	case itemActionBack:
		return ui.ErrGoBack
	case itemActionRun:
		return s.runItem(ws, item, cfg)
	case itemActionMove:
		return s.moveItem(ws, item)
	case itemActionRename:
		return s.renameItem(ws, item, siblings, refs)
	case itemActionDesc:
		return s.setItemDescription(ws, item)
	case itemActionDelete:
		return s.deleteItem(ws, item, refs)
	}
	return ui.ErrGoBack
}

// printItemPanel is the read-only summary shown before any item action. Every
// field comes from the list response already in hand, so opening an item costs
// no extra request.
func printItemPanel(ws fabric.Workspace, item fabric.Item, refs []config.ItemRef) {
	desc := item.Description
	if strings.TrimSpace(desc) == "" {
		desc = "none"
	}
	bg, _ := itemTypeColor(item.Type)

	fmt.Println()
	fmt.Println(infoStyle.Render(item.DisplayName))
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Type:", 13)),
		lipgloss.NewStyle().Foreground(bg).Bold(true).Render(item.Type))
	for _, row := range [][2]string{
		{"ID", item.ID},
		{"Description", desc},
		{"Workspace", ws.DisplayName},
	} {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth(row[0]+":", 13)), row[1])
	}
	if rendered := renderItemRefs(refs); rendered != "" {
		fmt.Printf("  %s\n%s", wsLabelStyle.Render("Used by:"), rendered)
	} else {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Used by:", 13)), "no futils config references")
	}
	fmt.Println()
}

// runItem hands the item to whichever existing flow knows how to run it. The
// environment is left blank in the context: this screen is reached from the
// tenant-wide workspace list, so there is no environment to name.
func (s *workspaceSession) runItem(ws fabric.Workspace, item fabric.Item, cfg config.Config) error {
	if _, ok := runnableItemLabels[item.Type]; !ok {
		fmt.Println(wsWarnStyle.Render(item.Type + " items have nothing to run."))
		return ui.ErrGoBack
	}
	ref := WorkspaceRef{Name: ws.DisplayName, ID: ws.ID}
	ctx := runContext{Customer: s.customer}

	switch item.Type {
	case "Notebook":
		return runNotebookOn(s.client, s.token, ref, item, cfg.Customers[s.customer], ctx)
	case "DataPipeline":
		return runPipelineOn(s.client, s.token, ref, item, ctx)
	case "SemanticModel":
		return refreshDatasetOn(s.client, s.token, ref,
			fabric.Dataset{ID: item.ID, Name: item.DisplayName}, ctx)
	}
	return ui.ErrGoBack
}

// moveItem hands the item to the existing move flow with the source workspace
// and item already chosen, so collision handling and the report rebind are the
// same code the top-level Move item entry uses.
func (s *workspaceSession) moveItem(ws fabric.Workspace, item fabric.Item) error {
	if !moveSupportedTypes[item.Type] {
		fmt.Println(wsWarnStyle.Render(
			"Move supports Report, SemanticModel and Notebook — not " + item.Type + "."))
		return ui.ErrGoBack
	}
	workspaces, err := s.client.ListWorkspaces(s.token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	return moveItemFrom(s.client, s.token, ws, item, workspaces, s.customer)
}

// renameItem renames the item in Fabric, then OFFERS to update the config
// entries that name it — it does not do so automatically.
//
// Item names in config are environment-agnostic by design: an override names an
// item looked up in whichever environment a deploy targets, and the same
// notebook exists in DEV, TEST and PROD. Renaming one copy and rewriting config
// would make the entry right here and wrong everywhere else, so the choice is
// the user's and the default is no.
func (s *workspaceSession) renameItem(ws fabric.Workspace, item fabric.Item, siblings []fabric.Item, refs []config.ItemRef) error {
	newName, err := wsPromptInput("New name for "+item.DisplayName, item.DisplayName)
	if err != nil {
		return err
	}
	if err := validateItemName(newName, siblings, item.DisplayName); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}

	ok, err := wsConfirm(fmt.Sprintf("Rename %q to %q?", item.DisplayName, newName))
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println(wsCancelStyle.Render("Cancelled."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Renaming...")
	spinner.Start()
	_, err = s.client.RenameItem(s.token, ws.ID, item.ID, newName)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("rename item: %w", err)
	}
	fmt.Println(infoStyle.Render("Renamed to " + newName + "."))

	return s.offerItemRefUpdate(item.DisplayName, newName, refs)
}

// offerItemRefUpdate asks whether the config entries naming the old item should
// follow the rename. Declining is the safe answer and therefore the default.
func (s *workspaceSession) offerItemRefUpdate(oldName, newName string, refs []config.ItemRef) error {
	if len(refs) == 0 {
		return ui.ErrGoBack
	}

	fmt.Println()
	fmt.Printf("Config entries naming %q:\n%s", oldName, renderItemRefs(refs))
	fmt.Println(wsWarnStyle.Render(
		"These are environment-agnostic: the same item name is looked up in every environment."))
	fmt.Println(wsLabelStyle.Render(
		"Only update them if you renamed the item in every environment, or intend to."))
	fmt.Println()

	ok, err := wsConfirm(fmt.Sprintf("Update %s to %q?", pluralItemRefs(len(refs)), newName))
	if err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			return ui.ErrGoBack
		}
		return err
	}
	if !ok {
		fmt.Println(wsLabelStyle.Render("Config left unchanged."))
		return ui.ErrGoBack
	}

	changed, err := s.updateConfig(func(cfg *config.Config) int {
		return config.RenameItemRefs(cfg, oldName, newName)
	})
	if err != nil {
		return fmt.Errorf("item was renamed in Fabric but config was NOT updated: %w", err)
	}
	fmt.Println(infoStyle.Render(fmt.Sprintf("Updated %s in config.", pluralItemRefs(changed))))
	return ui.ErrGoBack
}

func (s *workspaceSession) setItemDescription(ws fabric.Workspace, item fabric.Item) error {
	desc, err := wsPromptInput("Description for "+item.DisplayName+" (clear to remove)", item.Description)
	if err != nil {
		return err
	}
	if err := validateItemDescription(desc); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}
	if desc == item.Description {
		fmt.Println(wsLabelStyle.Render("Description unchanged."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Updating description...")
	spinner.Start()
	_, err = s.client.SetItemDescription(s.token, ws.ID, item.ID, desc)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("update description: %w", err)
	}
	fmt.Println(infoStyle.Render("Description updated."))
	return ui.ErrGoBack
}

// deleteItem removes one item. Data-bearing types take the typed confirmation,
// because deleting a lakehouse destroys its tables and files — the same
// distinction the deploy flow makes before removing orphans.
func (s *workspaceSession) deleteItem(ws fabric.Workspace, item fabric.Item, refs []config.ItemRef) error {
	destroysData := dataBearingItemTypes[item.Type]

	fmt.Println()
	if destroysData {
		fmt.Println(wsWarnStyle.Render(
			"Deleting a " + item.Type + " destroys its tables and files. This cannot be undone."))
	} else {
		fmt.Println(wsWarnStyle.Render("This cannot be undone from futils."))
	}
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Item:", 13)), item.DisplayName)
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Type:", 13)), item.Type)
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Workspace:", 13)), ws.DisplayName)
	if rendered := renderItemRefs(refs); rendered != "" {
		fmt.Printf("  %s\n%s", wsLabelStyle.Render("Config entries that name it:"), rendered)
	}
	fmt.Println()

	var ok bool
	var err error
	if destroysData {
		ok, err = wsConfirmTyped(fmt.Sprintf("Delete %q and its data?", item.DisplayName), wsDeleteWord)
	} else {
		ok, err = wsConfirm(fmt.Sprintf("Delete %q?", item.DisplayName))
	}
	if err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			fmt.Println(wsCancelStyle.Render("Cancelled."))
			return ui.ErrGoBack
		}
		return err
	}
	if !ok {
		fmt.Println(wsCancelStyle.Render("Cancelled — nothing was deleted."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Deleting " + item.DisplayName + "...")
	spinner.Start()
	err = s.client.DeleteItem(s.token, ws.ID, item.ID)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	fmt.Println(infoStyle.Render("Deleted " + item.DisplayName + "."))

	if len(refs) == 0 {
		return ui.ErrGoBack
	}
	// Same reasoning as rename: the config entries may still be valid for other
	// environments, so removing them is the user's call.
	remove, err := wsConfirm(fmt.Sprintf("Also remove %s from config?", pluralItemRefs(len(refs))))
	if err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			return ui.ErrGoBack
		}
		return err
	}
	if !remove {
		fmt.Println(wsLabelStyle.Render("Config left unchanged — the entries may still apply in other environments."))
		return ui.ErrGoBack
	}
	changed, err := s.updateConfig(func(cfg *config.Config) int {
		return config.RemoveItemRefs(cfg, item.DisplayName)
	})
	if err != nil {
		return fmt.Errorf("item was deleted in Fabric but config was NOT cleaned: %w", err)
	}
	fmt.Println(infoStyle.Render(fmt.Sprintf("Removed %s from config.", pluralItemRefs(changed))))
	return ui.ErrGoBack
}

func pluralItemRefs(n int) string {
	if n == 1 {
		return "1 config entry"
	}
	return fmt.Sprintf("%d config entries", n)
}
