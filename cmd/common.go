// Package cmd implements each user-facing subcommand plus the interactive
// main menu. Each file is a thin orchestration layer over internal/config,
// internal/fabric, and internal/ui — keep it that way: if a file reaches
// for complex logic, factor the logic into one of those packages and keep
// cmd as the wiring.
package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// formTheme is the single huh.Theme used across add/edit forms so the look
// matches Confirm() dialogs and the parameter form.
var formTheme = func() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true)
	t.Focused.FocusedButton = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(ui.AccentColor).Padding(0, 1)
	t.Focused.BlurredButton = lipgloss.NewStyle().Foreground(ui.DimColor).Padding(0, 1)
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(ui.AccentColor)
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(ui.AccentColor)
	return t
}()

// runFormStep is a one-field-at-a-time driver for huh forms. Treating
// aborts as "go back" matches the navigation model used everywhere else
// in the CLI — esc means "back to previous screen", never "fatal error".
func runFormStep(input *huh.Input) error {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	err := huh.NewForm(huh.NewGroup(input)).WithTheme(formTheme).WithKeyMap(km).Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ui.ErrGoBack
		}
		return err
	}
	return nil
}

// shortGUID truncates a GUID to its first 8 chars + ellipsis for compact
// display in menus and summaries; strings of 8 or fewer chars are unchanged.
func shortGUID(g string) string {
	if len(g) > 8 {
		return g[:8] + "…"
	}
	return g
}

// sortedCustomerNames gives us a stable menu order regardless of map
// iteration quirks — keeps the numbered menu predictable across runs.
func sortedCustomerNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.Customers))
	for name := range cfg.Customers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// WorkspaceRef pairs a Fabric workspace display name with its resolved
// UUID. Run / Refresh / Favourites all need both: the name for display
// in pickers and error messages, the UUID for API calls.
type WorkspaceRef struct {
	Name string
	ID   string
}

// authAndResolveWorkspaces authenticates the customer once and resolves
// every workspace name in `workspaceNames` to its Fabric UUID. Callers
// fan out from there — typically by calling ListNotebooks or
// ListDatasets per ref and tagging the results with the workspace.
// Returns an error if any workspace fails to resolve, on the
// principle that a partially-missing env almost always means a typo or
// permissions issue worth surfacing immediately rather than silently
// pruning.
//
// Uses one ListWorkspaces call + an in-memory map rather than N calls
// to GetWorkspaceID (each of which would re-fetch the full list).
func authAndResolveWorkspaces(client APIClient, customerName string, workspaceNames []string) (string, []WorkspaceRef, error) {
	if len(workspaceNames) == 0 {
		return "", nil, fmt.Errorf("env has no workspaces — run 'futils edit' to add at least one")
	}

	fmt.Println()
	fmt.Println(infoStyle.Render("Authenticating..."))
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return "", nil, fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println(infoStyle.Render("Authenticated."))
	fmt.Println()

	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return "", nil, fmt.Errorf("list workspaces: %w", err)
	}
	byName := make(map[string]string, len(workspaces))
	for _, ws := range workspaces {
		byName[ws.DisplayName] = ws.ID
	}

	refs := make([]WorkspaceRef, 0, len(workspaceNames))
	for _, name := range workspaceNames {
		id, ok := byName[name]
		if !ok {
			return "", nil, fmt.Errorf("workspace %q not found (check spelling and your access)", name)
		}
		refs = append(refs, WorkspaceRef{Name: name, ID: id})
	}
	return token, refs, nil
}

// TaggedNotebook is a Fabric notebook plus the workspace it lives in.
// Used by aggregating flows where an env spans multiple workspaces and
// the picker needs to disambiguate same-named notebooks.
type TaggedNotebook struct {
	Notebook  fabric.Item
	Workspace WorkspaceRef
}

// aggregateNotebooks fans ListNotebooks across every workspace ref,
// tagging each notebook with its origin. Errors from individual
// workspaces are wrapped with the workspace name so the user knows
// which one failed.
func aggregateNotebooks(client APIClient, token string, refs []WorkspaceRef) ([]TaggedNotebook, error) {
	spinner := ui.NewSpinner("Listing notebooks...")
	spinner.Start()
	defer spinner.Stop()

	var all []TaggedNotebook
	for _, ref := range refs {
		nbs, err := client.ListNotebooks(token, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list notebooks in %s: %w", ref.Name, err)
		}
		for _, nb := range nbs {
			all = append(all, TaggedNotebook{Notebook: nb, Workspace: ref})
		}
	}
	if len(all) == 0 {
		names := make([]string, len(refs))
		for i, r := range refs {
			names[i] = r.Name
		}
		return nil, fmt.Errorf("no notebooks found in workspaces: %s", strings.Join(names, ", "))
	}
	return all, nil
}
