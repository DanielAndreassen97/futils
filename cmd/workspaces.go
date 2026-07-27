package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// Swap hooks for testing, same pattern as the move flow: tests replace these to
// drive the prompts without spinning up bubbletea.
var (
	wsFilterPicker = ui.FilterMenu
	wsNumberPicker = ui.NumberMenu
	wsPromptInput  = promptInputWithValue
	wsConfirm      = ui.Confirm
	wsConfirmTyped = ui.ConfirmTyped
)

// Sentinel picker values. Prefixed so they can never collide with a Fabric
// workspace or capacity ID.
const (
	wsActionCreate = "__create"
	wsActionRename = "__rename"
	wsActionDesc   = "__description"
	wsActionDelete = "__delete"
	wsActionBack   = "__back"
	wsCapacitySkip = "__no_capacity"

	// wsDeleteWord is what the user must type to delete a workspace. Deleting
	// takes every item in the workspace with it, so a single keystroke is too
	// cheap a confirmation.
	wsDeleteWord = "Yes"

	// wsNameColW aligns the name column in the workspace picker so the capacity
	// annotations line up.
	wsNameColW = 44

	// wsRoleAdmin is the only role that may rename or delete a workspace.
	wsRoleAdmin = "Admin"
	// wsRoleNone is futils' own bucket for a workspace that came back under no
	// role at all — a personal workspace, or access granted some other way. Not
	// a value Fabric accepts in a roles filter.
	wsRoleNone = "None"
)

// wsRoles are Fabric's workspace roles, most capable first. The order is the
// order the picker groups them in, and each one costs a roles-filtered list
// call. Spelling matters: these strings go straight into the API query.
var wsRoles = []string{wsRoleAdmin, "Member", "Contributor", "Viewer"}

var (
	wsLabelStyle = lipgloss.NewStyle().Foreground(ui.DimColor)
	wsWarnStyle  = lipgloss.NewStyle().Foreground(ui.WarnColor)
	// wsCancelStyle marks the lines that say nothing happened. They scroll past
	// in the same output as the lines that did, so they get their own colour
	// rather than reading as one more neutral status message.
	wsCancelStyle = lipgloss.NewStyle().Foreground(ui.StopColor).Bold(true)
)

// wsRoleBarPalette is the role heading's colour ladder: a full-width inverted
// bar, brightest for Admin and darkest for Viewer, so the heading breaks the
// list physically instead of blending into it. Brightness carries the hierarchy,
// which keeps the licence-tier colours free to mean something else entirely.
//
// Roles with no entry fall back to the grey bar — a workspace reported under no
// role has no place on a ladder of authority.
var wsRoleBarPalette = map[string]struct{ bg, fg lipgloss.Color }{
	wsRoleAdmin:   {"#22c55e", "#08120b"},
	"Member":      {"#16a34a", "#f0fdf4"},
	"Contributor": {"#15803d", "#dcfce7"},
	"Viewer":      {"#14532d", "#bbf7d0"},
}

var wsRoleBarFallback = struct{ bg, fg lipgloss.Color }{"#3a4547", "#d7dfe0"}

// wsRoleHeader is a picker row that is a role heading rather than a workspace.
// It rides in FilterOption.Meta so the renderer knows which bar colour to use
// without parsing the label back apart.
type wsRoleHeader struct{ Role string }

// wsBarWidth is how wide a role bar is drawn: the live terminal minus the
// two-column indent the picker adds to every row.
func wsBarWidth() int {
	return terminalWidth(80, 24) - 2
}

// renderRoleBar draws the full-width heading bar for one role.
func renderRoleBar(role, label string) string {
	c, ok := wsRoleBarPalette[role]
	if !ok {
		c = wsRoleBarFallback
	}
	return renderSectionBar(c.bg, c.fg, label)
}

// renderSectionBar is the full-width inverted heading used to break a long
// picker into sections — roles in the workspace list, item types in the item
// browser. One definition so the two lists cannot drift apart on width or inset.
func renderSectionBar(bg, fg lipgloss.Color, label string) string {
	return lipgloss.NewStyle().
		Background(bg).Foreground(fg).Bold(true).
		Width(wsBarWidth()).
		Render(" " + label)
}

// Workspaces is the top-level entry point for the workspace-management flow.
func Workspaces(configPath string) error {
	return WorkspacesWithAPI(configPath, DefaultAPI)
}

// WorkspacesWithAPI is the testable entry point. The customer step is not
// decoration: the customer name IS the auth profile, exactly as in every other
// flow. Everything after authentication is tenant-wide — the picker lists every
// workspace the token can see, not just the ones in the customer's config.
func WorkspacesWithAPI(configPath string, client APIClient) error {
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

	s := &workspaceSession{
		client:     client,
		token:      token,
		configPath: configPath,
		customer:   customerName,
	}
	return s.loop()
}

// workspaceSession carries what every action in the flow needs. Capacities are
// fetched at most once per session — they change rarely and the list costs a
// scope some tenants may not grant.
type workspaceSession struct {
	client     APIClient
	token      string
	configPath string
	customer   string

	capacities []fabric.Capacity
	capsLoaded bool
	capsErr    error

	// workspaces is the tenant listing from the most recent load(), kept so a
	// flow that needs the whole list — the move destination picker — does not
	// refetch what the screen it was launched from already has. Refreshed by
	// load(), which only runs when something invalidated the list.
	workspaces []fabric.Workspace
}

// loop shows the workspace picker until the user backs out. The list is
// reloaded on every pass: after a rename or delete a stale list would show a
// name that no longer exists, and that is exactly the moment a user looks again.
func (s *workspaceSession) loop() error {
	var (
		workspaces []fabric.Workspace
		roleOf     map[string]string
		err        error
		// The tenant listing costs six requests — the workspace list plus one
		// roles-filtered list per role plus capacities. Only refetch it when
		// something has actually changed it: creating, renaming or deleting a
		// workspace. Coming back from an item does not.
		stale = true
	)
	for {
		if stale {
			if workspaces, roleOf, err = s.load(); err != nil {
				return err
			}
			stale = false
		}

		choice, err := s.pick(workspaces, roleOf)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}

		if choice == wsActionCreate {
			err := s.create(workspaces)
			// A create that got as far as prompting may have landed even if it
			// then failed to register in config, so refetch either way.
			stale = true
			if err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
			continue
		}

		selected, ok := workspaceByID(workspaces, choice)
		if !ok {
			// The list changed between render and selection — harmless.
			stale = true
			continue
		}
		mutated, err := s.manage(selected, roleOf[selected.ID])
		if mutated {
			stale = true
		}
		if err != nil && !errors.Is(err, ui.ErrGoBack) {
			return err
		}
	}
}

// load fetches the workspace list and the caller's role in each one, under a
// single spinner. Fabric's workspace list does not carry your role, and the
// alternative — a roleAssignments lookup per workspace — is one request per row.
// A roles-filtered list per role is four requests for the whole tenant.
func (s *workspaceSession) load() ([]fabric.Workspace, map[string]string, error) {
	spinner := ui.NewSpinner("Loading workspaces...")
	spinner.Start()
	workspaces, err := s.client.ListWorkspaces(s.token)
	if err != nil {
		spinner.Stop()
		return nil, nil, fmt.Errorf("list workspaces: %w", err)
	}

	roleOf := make(map[string]string, len(workspaces))
	var roleErr error
	for _, role := range wsRoles {
		inRole, err := s.client.ListWorkspacesByRole(s.token, role)
		if err != nil {
			roleErr = err
			break
		}
		for _, ws := range inRole {
			roleOf[ws.ID] = role
		}
	}

	// Capacities are session state, fetched here so the picker and the detail
	// panel can name a workspace's capacity from the first screen. A failure is
	// remembered and surfaces only where it matters — the create flow.
	s.fetchCapacities()
	spinner.Stop()

	if roleErr != nil {
		// Without roles the flow still works: the grouping collapses and the
		// pre-flight check stops helping, so Fabric answers 403 instead. That
		// beats failing a read-only listing outright.
		fmt.Println(wsWarnStyle.Render("Could not read your workspace roles: " + roleErr.Error()))
		fmt.Println(wsLabelStyle.Render("Rename and delete will be attempted and may be refused by Fabric."))
		for _, ws := range workspaces {
			roleOf[ws.ID] = wsRoleAdmin
		}
	}
	s.workspaces = workspaces
	return workspaces, roleOf, nil
}

// pick renders the workspace picker: a create row, then the workspaces grouped
// under their role heading.
func (s *workspaceSession) pick(workspaces []fabric.Workspace, roleOf map[string]string) (string, error) {
	options := []ui.FilterOption{{Label: "+ Create new workspace", Value: wsActionCreate}}
	options = append(options, groupWorkspacesByRole(workspaces, roleOf, s.capacities)...)

	return wsFilterPicker("Select a workspace", options, renderWorkspaceRow)
}

// renderWorkspaceRow draws one picker row: a role heading bar, or a workspace
// with its licence tier in a fixed column so the tiers and SKUs line up down
// the list.
//
// The cursor row is the arrow plus accent text used by every list in the TUI
// (see ui.CursorPointer). The whole row takes the accent colour rather than
// keeping its tier colour: a row that is half accent and half orange reads as
// two rows, and the FilterMenu contract asks for a uniform highlight.
func renderWorkspaceRow(opt ui.FilterOption, selected bool) string {
	if opt.IsHeader {
		hdr, _ := opt.Meta.(wsRoleHeader)
		return renderRoleBar(hdr.Role, opt.Label)
	}
	lead := ui.CursorPointer(selected)
	tier, ok := opt.Meta.(workspaceTier)
	if !ok {
		return lead + ui.CursorLabel(opt.Label, selected)
	}
	if selected {
		return lead + ui.CursorLabel(ui.FitWidth(opt.Label, wsNameColW)+"  "+tier.plain(), true)
	}
	return lead + ui.FitWidth(opt.Label, wsNameColW) + "  " + tier.render()
}

// groupWorkspacesByRole turns the flat workspace list into picker rows grouped
// under a heading per role, in descending order of what the role lets you do.
// Grouping by role rather than sorting one long list answers the question the
// user actually has — "which of these can I act on?" — before they read a name.
//
// Workspaces with no role land in a final group: a personal workspace has no
// role assignment, and neither does access inherited some other way.
func groupWorkspacesByRole(workspaces []fabric.Workspace, roleOf map[string]string, caps []fabric.Capacity) []ui.FilterOption {
	byRole := map[string][]fabric.Workspace{}
	for _, ws := range workspaces {
		role := roleOf[ws.ID]
		if role == "" {
			role = wsRoleNone
		}
		byRole[role] = append(byRole[role], ws)
	}

	var out []ui.FilterOption
	for _, role := range append(append([]string{}, wsRoles...), wsRoleNone) {
		group := byRole[role]
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			return strings.ToLower(group[i].DisplayName) < strings.ToLower(group[j].DisplayName)
		})
		out = append(out, ui.FilterOption{
			Label:    fmt.Sprintf("%s · %d", wsRoleHeading(role), len(group)),
			IsHeader: true,
			Meta:     wsRoleHeader{Role: role},
		})
		for _, ws := range group {
			out = append(out, ui.FilterOption{
				Label: ws.DisplayName,
				Value: ws.ID,
				Meta:  classifyWorkspace(ws, caps),
			})
		}
	}
	return out
}

// wsRoleHeading is the section title for a role. Upper case so a heading never
// reads as one more workspace name.
func wsRoleHeading(role string) string {
	if role == wsRoleNone {
		return "NO WORKSPACE ROLE"
	}
	return strings.ToUpper(role)
}

// roleDisplay names a role in the detail panel. "Contributor" and "Viewer" are
// both blocked from renaming, but they are not the same thing, and a panel that
// flattened them to "read-only" would hide which one you need to ask about.
func roleDisplay(role string) string {
	if role == "" || role == wsRoleNone {
		return "none reported"
	}
	return role
}

func roleWithArticle(role string) string {
	switch role {
	case "", wsRoleNone:
		return "reported under no role at all"
	case wsRoleAdmin, "Member":
		return "a " + role
	}
	return "a " + role
}

// manage is the screen you stay on while working inside one workspace: its own
// actions above its items. It loops, so acting on an item — or backing out of
// one — returns here rather than dropping you back to the tenant-wide list you
// came through. Only a workspace-level change, or Back, leaves.
//
// Returns mutated=true when something happened that invalidates the caller's
// workspace list, so the caller knows whether its six-request listing needs
// refetching.
func (s *workspaceSession) manage(ws fabric.Workspace, role string) (mutated bool, err error) {
	isAdmin := role == wsRoleAdmin

	spinner := ui.NewSpinner("Loading " + ws.DisplayName + "...")
	spinner.Start()
	detail, err := s.client.GetWorkspace(s.token, ws.ID)
	if err != nil {
		spinner.Stop()
		return false, fmt.Errorf("get workspace %q: %w", ws.DisplayName, err)
	}
	items, itemsErr := s.client.ListItems(s.token, ws.ID)
	spinner.Stop()

	for {
		cfg, err := config.Load(s.configPath)
		if err != nil {
			return false, err
		}
		refs := config.FindWorkspaceRefs(cfg, detail.DisplayName)

		s.printPanel(detail, items, itemsErr, refs, role)

		choice, err := wsFilterPicker("Manage "+detail.DisplayName,
			workspaceScreenOptions(items, isAdmin), renderWorkspaceScreenRow)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return false, ui.ErrGoBack
			}
			return false, err
		}
		if choice == wsActionBack {
			return false, ui.ErrGoBack
		}

		// An item was chosen rather than one of the pinned workspace actions.
		if item, ok := itemByID(items, choice); ok {
			changed, err := s.manageItem(detail, item, items)
			if err != nil && !errors.Is(err, ui.ErrGoBack) {
				return false, err
			}
			if changed {
				// The item list is stale — but only that. The tenant list, the
				// workspace's own metadata and the capacities are all untouched.
				items, itemsErr = s.client.ListItems(s.token, ws.ID)
			}
			continue
		}

		if !isAdmin {
			fmt.Println()
			fmt.Println(wsWarnStyle.Render(fmt.Sprintf(
				"Rename, description and delete all require the Admin workspace role — you are %s.", roleWithArticle(role))))
			fmt.Println(wsLabelStyle.Render("Ask a workspace admin to grant it, then try again."))
			continue
		}

		// Every remaining action changes the workspace itself, so the list this
		// screen was reached from is now wrong either way.
		switch choice {
		case wsActionRename:
			return true, s.rename(detail, refs)
		case wsActionDesc:
			return true, s.setDescription(detail)
		case wsActionDelete:
			return true, s.delete(detail, items, itemsErr, refs)
		}
		return false, ui.ErrGoBack
	}
}

// workspaceScreenOptions builds the one screen you land on inside a workspace:
// the workspace's own actions pinned at the top, then every item it holds,
// grouped by type.
//
// One screen rather than an action menu with a "browse items" entry, because the
// items are what you came to look at. Pinning the actions means typing filters
// the items while Rename stays one arrow-up away — see ui.FilterOption.Pinned.
func workspaceScreenOptions(items []fabric.Item, isAdmin bool) []ui.FilterOption {
	actions := []ui.FilterOption{
		{Label: "Rename workspace", Value: wsActionRename, Pinned: true},
		{Label: "Edit description", Value: wsActionDesc, Pinned: true},
		{Label: "Delete workspace", Value: wsActionDelete, Pinned: true},
		{Label: "Back", Value: wsActionBack, Pinned: true},
	}
	if !isAdmin {
		for i := range actions[:3] {
			actions[i].Meta = wsPinnedAction{Badge: "NEEDS ADMIN"}
		}
	}
	return append(actions, groupItemsByType(items)...)
}

// wsPinnedAction carries a pinned row's badge. Only set when the action is
// unavailable, so the zero value means "no badge".
type wsPinnedAction struct{ Badge string }

// renderWorkspaceScreenRow draws a row of the combined workspace screen: a
// pinned workspace action, a coloured item-type heading, or an item.
func renderWorkspaceScreenRow(opt ui.FilterOption, selected bool) string {
	if opt.IsHeader {
		hdr, _ := opt.Meta.(itemTypeHeader)
		bg, fg := itemTypeColor(hdr.Type)
		return renderSectionBar(bg, fg, opt.Label)
	}

	lead := ui.CursorPointer(selected)
	if opt.Pinned {
		row := lead + ui.CursorLabel(opt.Label, selected)
		if action, ok := opt.Meta.(wsPinnedAction); ok && action.Badge != "" {
			row += "  " + wsWarnStyle.Render("["+action.Badge+"]")
		}
		return row
	}
	return lead + ui.CursorLabel(opt.Label, selected)
}

func itemByID(items []fabric.Item, id string) (fabric.Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return fabric.Item{}, false
}

// printPanel is the read-only summary shown before any action. Item counts come
// last of the API-backed lines because they are the ones that make a delete
// decision, and a failure to read them must not block the flow.
func (s *workspaceSession) printPanel(ws fabric.Workspace, items []fabric.Item, itemsErr error, refs []config.WorkspaceRef, role string) {
	desc := ws.Description
	if strings.TrimSpace(desc) == "" {
		desc = "none"
	}
	itemLine := itemTypeCounts(items)
	if itemsErr != nil {
		itemLine = "unavailable (" + itemsErr.Error() + ")"
	}

	fmt.Println()
	fmt.Println(infoStyle.Render(ws.DisplayName))
	tier := classifyWorkspace(ws, s.capacities)
	for _, row := range [][2]string{
		{"ID", ws.ID},
		{"Description", desc},
		{"Licence", tier.Name},
		{"Capacity", capacityLabel(s.capacities, ws.CapacityID)},
		{"Your role", roleDisplay(role)},
		{"Items", itemLine},
	} {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth(row[0]+":", 13)), row[1])
	}
	if rendered := renderWorkspaceRefs(refs); rendered != "" {
		fmt.Printf("  %s\n%s", wsLabelStyle.Render("Used by:"), rendered)
	} else {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Used by:", 13)), "no futils config references")
	}
	fmt.Println()
}

// rename changes the display name in Fabric, then repairs every config
// reference. The order matters: config must never claim a name Fabric does not
// have, so a failed PATCH leaves the config exactly as it was.
func (s *workspaceSession) rename(ws fabric.Workspace, refs []config.WorkspaceRef) error {
	workspaces, err := s.client.ListWorkspaces(s.token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	newName, err := wsPromptInput("New name for "+ws.DisplayName, ws.DisplayName)
	if err != nil {
		return err
	}
	if err := validateWorkspaceName(newName, workspaces, ws.DisplayName); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}

	fmt.Println()
	if rendered := renderWorkspaceRefs(refs); rendered != "" {
		fmt.Printf("Config references to %q:\n%s", ws.DisplayName, rendered)
		fmt.Println(wsLabelStyle.Render(fmt.Sprintf("%s will be updated to %q.", pluralRefs(len(refs)), newName)))
	} else {
		fmt.Println(wsLabelStyle.Render("No futils config references — nothing to update."))
	}
	fmt.Println()

	ok, err := wsConfirm(fmt.Sprintf("Rename %q to %q?", ws.DisplayName, newName))
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println(wsCancelStyle.Render("Cancelled."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Renaming...")
	spinner.Start()
	_, err = s.client.RenameWorkspace(s.token, ws.ID, newName)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("rename workspace: %w", err)
	}
	fmt.Println(infoStyle.Render("Renamed to " + newName + "."))

	changed, err := s.updateConfig(func(cfg *config.Config) int {
		return config.RenameWorkspaceRefs(cfg, ws.DisplayName, newName)
	})
	if err != nil {
		return fmt.Errorf("workspace was renamed in Fabric but config was NOT updated (%d references still say %q): %w",
			len(refs), ws.DisplayName, err)
	}
	if changed > 0 {
		fmt.Println(infoStyle.Render(fmt.Sprintf("Updated %s in config.", pluralRefs(changed))))
	}
	return ui.ErrGoBack
}

// setDescription replaces the description. No config impact — futils never
// stores workspace descriptions.
func (s *workspaceSession) setDescription(ws fabric.Workspace) error {
	desc, err := wsPromptInput("Description for "+ws.DisplayName+" (clear to remove)", ws.Description)
	if err != nil {
		return err
	}
	if err := validateWorkspaceDescription(desc); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}
	if desc == ws.Description {
		fmt.Println(wsLabelStyle.Render("Description unchanged."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Updating description...")
	spinner.Start()
	_, err = s.client.SetWorkspaceDescription(s.token, ws.ID, desc)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("update description: %w", err)
	}
	fmt.Println(infoStyle.Render("Description updated."))
	return ui.ErrGoBack
}

// delete removes the workspace and every item in it, then drops the config
// references. The blast radius is printed in full before the typed prompt.
func (s *workspaceSession) delete(ws fabric.Workspace, items []fabric.Item, itemsErr error, refs []config.WorkspaceRef) error {
	fmt.Println()
	fmt.Println(wsWarnStyle.Render("Deleting a workspace deletes every item inside it. This cannot be undone."))
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Workspace:", 13)), ws.DisplayName)
	if itemsErr != nil {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Items:", 13)), wsWarnStyle.Render("could not be listed — delete blind at your own risk"))
	} else {
		fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Items:", 13)), itemTypeCounts(items))
	}
	if rendered := renderWorkspaceRefs(refs); rendered != "" {
		fmt.Printf("  %s\n%s", wsLabelStyle.Render("Config references (will be removed):"), rendered)
	}
	fmt.Println()

	ok, err := wsConfirmTyped(fmt.Sprintf("Delete %q and everything in it?", ws.DisplayName), wsDeleteWord)
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

	spinner := ui.NewSpinner("Deleting " + ws.DisplayName + "...")
	spinner.Start()
	err = s.client.DeleteWorkspace(s.token, ws.ID)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	fmt.Println(infoStyle.Render("Deleted " + ws.DisplayName + "."))

	changed, err := s.updateConfig(func(cfg *config.Config) int {
		return config.RemoveWorkspaceRefs(cfg, ws.DisplayName)
	})
	if err != nil {
		return fmt.Errorf("workspace was deleted in Fabric but config was NOT cleaned (%d stale references to %q remain): %w",
			len(refs), ws.DisplayName, err)
	}
	if changed > 0 {
		fmt.Println(infoStyle.Render(fmt.Sprintf("Removed %s from config.", pluralRefs(changed))))
		warnEmptyEnvironments(s.configPath)
	}
	return ui.ErrGoBack
}

// create walks name → description → capacity → confirm, then offers to register
// the new workspace in one of the current customer's environments.
func (s *workspaceSession) create(existing []fabric.Workspace) error {
	name, err := wsPromptInput("Workspace name", "")
	if err != nil {
		return err
	}
	if err := validateWorkspaceName(name, existing, ""); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}

	desc, err := wsPromptInput("Description (optional)", "")
	if err != nil {
		return err
	}
	if err := validateWorkspaceDescription(desc); err != nil {
		fmt.Println(wsWarnStyle.Render(err.Error()))
		return ui.ErrGoBack
	}

	capID, err := s.pickCapacity()
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(infoStyle.Render("New workspace"))
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Name:", 13)), name)
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Description:", 13)), orNone(desc))
	fmt.Printf("  %s %s\n", wsLabelStyle.Render(ui.FitWidth("Capacity:", 13)), capacityLabel(s.capacities, capID))
	if capID == "" {
		fmt.Println(wsWarnStyle.Render("  Without a capacity you cannot create Fabric items in this workspace."))
	}
	fmt.Println()

	ok, err := wsConfirm("Create this workspace?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println(wsCancelStyle.Render("Cancelled."))
		return ui.ErrGoBack
	}

	spinner := ui.NewSpinner("Creating " + name + "...")
	spinner.Start()
	ws, err := s.client.CreateWorkspace(s.token, name, desc, capID)
	spinner.Stop()
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	fmt.Println(infoStyle.Render("Created " + ws.DisplayName + "."))

	return s.offerRegister(ws.DisplayName)
}

// offerRegister asks whether the new workspace should join one of the current
// customer's environments, so it is usable from the other flows immediately.
func (s *workspaceSession) offerRegister(name string) error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[s.customer]
	if !ok || len(customer.Environments) == 0 {
		fmt.Println(wsLabelStyle.Render(fmt.Sprintf("%s has no environments yet — add one under Manage customers to use this workspace.", s.customer)))
		return ui.ErrGoBack
	}

	add, err := wsConfirm(fmt.Sprintf("Add %q to %s?", name, s.customer))
	if err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			return ui.ErrGoBack
		}
		return err
	}
	if !add {
		return ui.ErrGoBack
	}

	options := make([]ui.MenuOption, 0, len(customer.Environments))
	for _, env := range customer.Environments {
		options = append(options, ui.MenuOption{
			Label:       env.Alias,
			Value:       env.Alias,
			Description: strings.Join(env.Workspaces, ", "),
		})
	}
	alias, err := wsNumberPicker("Which environment?", options)
	if err != nil {
		if errors.Is(err, ui.ErrGoBack) {
			return ui.ErrGoBack
		}
		return err
	}

	if _, err := s.updateConfig(func(c *config.Config) int {
		customer := c.Customers[s.customer]
		for i := range customer.Environments {
			if customer.Environments[i].Alias != alias {
				continue
			}
			customer.Environments[i].Workspaces = append(customer.Environments[i].Workspaces, name)
			c.Customers[s.customer] = customer
			return 1
		}
		return 0
	}); err != nil {
		return fmt.Errorf("workspace was created but config was NOT updated: %w", err)
	}
	fmt.Println(infoStyle.Render(fmt.Sprintf("Added %s to %s / %s.", name, s.customer, alias)))
	return ui.ErrGoBack
}

// pickCapacity returns the chosen capacity ID, or "" for none. A tenant that
// does not grant Capacity.Read.All still has to be able to create a workspace,
// so an unreadable list degrades to an explicit no-capacity choice rather than
// blocking the flow.
func (s *workspaceSession) pickCapacity() (string, error) {
	caps, err := s.loadCapacities()
	if err != nil || len(caps) == 0 {
		reason := "No capacities are visible to you."
		if err != nil {
			reason = "Could not list capacities: " + err.Error()
		}
		fmt.Println(wsWarnStyle.Render(reason))
		fmt.Println(wsLabelStyle.Render("The workspace can be created without one and assigned a capacity in the Fabric portal later."))
		ok, cErr := wsConfirm("Create without a capacity?")
		if cErr != nil {
			return "", cErr
		}
		if !ok {
			return "", ui.ErrGoBack
		}
		return "", nil
	}

	// Active capacities first — an inactive one cannot run anything.
	sorted := append([]fabric.Capacity(nil), caps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].State == "Active" && sorted[j].State != "Active"
	})

	options := make([]ui.FilterOption, 0, len(sorted)+1)
	for _, c := range sorted {
		label := fmt.Sprintf("%s · %s · %s", c.DisplayName, c.SKU, c.Region)
		if c.State != "Active" {
			label += "  [" + c.State + " — items cannot run]"
		}
		options = append(options, ui.FilterOption{Label: label, Value: c.ID})
	}
	options = append(options, ui.FilterOption{Label: "Skip — no capacity", Value: wsCapacitySkip})

	choice, err := wsFilterPicker("Select a capacity", options, ui.DefaultFilterRowRenderer)
	if err != nil {
		return "", err
	}
	if choice == wsCapacitySkip {
		return "", nil
	}
	return choice, nil
}

// fetchCapacities loads the capacity list once per session, remembering a
// failure so a tenant that does not grant Capacity.Read.All is not retried on
// every pass. Callers that only render a name ignore the error; the create flow
// reads it via loadCapacities.
func (s *workspaceSession) fetchCapacities() {
	if s.capsLoaded {
		return
	}
	s.capacities, s.capsErr = s.client.ListCapacities(s.token)
	s.capsLoaded = true
}

func (s *workspaceSession) loadCapacities() ([]fabric.Capacity, error) {
	s.fetchCapacities()
	return s.capacities, s.capsErr
}

// updateConfig applies mutate to a freshly loaded config and saves it only when
// something changed. Loading fresh (rather than reusing an earlier read) keeps a
// long-lived session from writing back a stale file.
func (s *workspaceSession) updateConfig(mutate func(*config.Config) int) (int, error) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return 0, err
	}
	changed := mutate(&cfg)
	if changed == 0 {
		return 0, nil
	}
	if err := config.Save(s.configPath, cfg); err != nil {
		return 0, err
	}
	return changed, nil
}

// warnEmptyEnvironments names any environment left without workspaces. Such an
// environment is a valid config state but cannot run notebooks, and silently
// leaving one behind is how a later flow fails for no visible reason.
func warnEmptyEnvironments(configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	for _, customer := range sortedKeys(cfg.Customers) {
		for _, env := range cfg.Customers[customer].Environments {
			if len(env.Workspaces) == 0 {
				fmt.Println(wsWarnStyle.Render(fmt.Sprintf("%s / %s has no workspaces left — add one before using it.", customer, env.Alias)))
			}
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── validation ────────────────────────────────────────────────────────────

// validateWorkspaceName enforces Fabric's documented limits client-side, plus a
// collision check against the workspaces we can see. That check is a courtesy:
// Fabric requires tenant-wide uniqueness and remains the authority, since the
// list only covers workspaces this token can read.
//
// current is the name being renamed away from — empty when creating. It is
// skipped in the collision check so an unchanged name reports "must differ"
// instead of the confusing "already taken".
func validateWorkspaceName(name string, existing []fabric.Workspace, current string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("workspace name cannot be empty")
	}
	if len([]rune(name)) > fabric.MaxWorkspaceNameLen {
		return fmt.Errorf("workspace name cannot be longer than %d characters", fabric.MaxWorkspaceNameLen)
	}
	if strings.EqualFold(name, fabric.ReservedWorkspaceName) {
		return fmt.Errorf("%q is a reserved workspace name in Fabric", fabric.ReservedWorkspaceName)
	}
	if current != "" && name == current {
		return errors.New("the new name must differ from the current one")
	}
	for _, ws := range existing {
		if ws.DisplayName == current {
			continue
		}
		if ws.DisplayName == name {
			return fmt.Errorf("a workspace named %q already exists", name)
		}
	}
	return nil
}

func validateWorkspaceDescription(desc string) error {
	if len([]rune(desc)) > fabric.MaxWorkspaceDescLen {
		return fmt.Errorf("description cannot be longer than %d characters", fabric.MaxWorkspaceDescLen)
	}
	return nil
}

// ── rendering helpers ─────────────────────────────────────────────────────

// itemTypeCounts summarises a workspace's contents as "3 Notebook, 2 Lakehouse",
// biggest group first so the most consequential number is the one you read
// before confirming a delete. Ties break on type name for a stable line.
func itemTypeCounts(items []fabric.Item) string {
	if len(items) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, it := range items {
		typ := it.Type
		if typ == "" {
			typ = "Unknown"
		}
		counts[typ]++
	}
	types := sortedKeys(counts)
	sort.SliceStable(types, func(i, j int) bool { return counts[types[i]] > counts[types[j]] })

	parts := make([]string, 0, len(types))
	for _, typ := range types {
		parts = append(parts, fmt.Sprintf("%d %s", counts[typ], typ))
	}
	return strings.Join(parts, ", ")
}

// capacityLabel names the capacity a workspace sits on. An ID we cannot resolve
// is reported as the raw ID rather than "none" — the workspace does have a
// capacity. The two unresolved cases are worded differently on purpose: an
// empty list means we never managed to look, which is not the same as being
// told no.
func capacityLabel(caps []fabric.Capacity, id string) string {
	if id == "" {
		return "none"
	}
	for _, c := range caps {
		if c.ID == id {
			return fmt.Sprintf("%s · %s · %s", c.DisplayName, c.SKU, c.Region)
		}
	}
	if len(caps) == 0 {
		return id + " (capacity list unavailable)"
	}
	return id + " (no access to this capacity)"
}

// renderWorkspaceRefs lists the config references to a workspace, one indented
// line each. Returns the empty string for no references so callers can choose
// their own wording for that case.
func renderWorkspaceRefs(refs []config.WorkspaceRef) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, ref := range refs {
		where := ref.Customer + " / " + ref.Environment
		what := ref.Kind.String()
		switch ref.Kind {
		case config.RefDeployTarget:
			what = fmt.Sprintf("deploy mapping %q", ref.Mapping)
		case config.RefDeployBaseline:
			what = fmt.Sprintf("baseline workspace for %q", ref.Mapping)
		}
		fmt.Fprintf(&b, "    %s  %s\n", ui.FitWidth(where, 30), wsLabelStyle.Render(what))
	}
	return b.String()
}

func pluralRefs(n int) string {
	if n == 1 {
		return "1 reference"
	}
	return fmt.Sprintf("%d references", n)
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// promptInputWithValue is a single-field text prompt pre-filled with initial.
// The bound variable must be seeded BEFORE Value() — huh captures the pointer's
// current value at that moment, so seeding afterwards renders an empty field.
func promptInputWithValue(title, initial string) (string, error) {
	v := initial
	input := huh.NewInput().Title(title).Value(&v)
	if err := runFormStep(input); err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

func workspaceByID(workspaces []fabric.Workspace, id string) (fabric.Workspace, bool) {
	for _, ws := range workspaces {
		if ws.ID == id {
			return ws, true
		}
	}
	return fabric.Workspace{}, false
}
