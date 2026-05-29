package cmd

import (
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// menuValueSave is a sentinel so we can tell "user picked Save and exit"
// apart from "user picked a notebook row". Prefixed with __ to avoid
// collision with any real notebook displayName.
const menuValueSave = "__save"

// Favorites walks the user through pinning notebooks and their preferred
// parameters for the currently-selected customer. This is an interactive-
// only flow — every step is a prompt, nothing is mutated on disk until
// the user either drills into param-editing (incremental save) or picks
// "Save and exit".
//
// Design note: favourites are per-customer, not per-environment. Notebook
// display names are stable across DEV/TEST/PROD in practice; their IDs
// are not. Storing names lets the same favourite apply to every env.
func Favorites(configPath string) error {
	return FavoritesWithAPI(configPath, DefaultAPI)
}

// FavoritesWithAPI is the flow with an injectable APIClient for tests.
func FavoritesWithAPI(configPath string, client APIClient) error {
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

	// Favourites are per-customer, not per-environment — notebook names are
	// the same across DEV/TEST/PROD. We pick the first env that has any
	// workspaces; removing all workspaces from env[0] (now possible via
	// edit submenu) used to wedge the whole favourites flow.
	if len(customer.Environments) == 0 {
		return fmt.Errorf("no environments configured for customer %q — run 'futils edit' to add one", customerName)
	}
	var envWorkspaces []string
	for _, e := range customer.Environments {
		if len(e.Workspaces) > 0 {
			envWorkspaces = e.Workspaces
			break
		}
	}
	if len(envWorkspaces) == 0 {
		return fmt.Errorf("no workspaces configured on any env for %q — run 'futils edit' to add one", customerName)
	}
	token, refs, err := authAndResolveWorkspaces(client, customerName, envWorkspaces)
	if err != nil {
		return err
	}
	tagged, err := aggregateNotebooks(client, token, refs)
	if err != nil {
		return err
	}

	// Dedupe by display name — favourites pin by name, so if the same
	// notebook name lives in two workspaces both versions count as the
	// favourite. notebookByName holds the *first* occurrence; the param-
	// filter drill-down later uses it to fetch the ipynb. Param cells are
	// virtually always identical across same-named notebooks in different
	// workspaces, so the first-occurrence pick is the right default.
	notebookByName := make(map[string]TaggedNotebook, len(tagged))
	for _, t := range tagged {
		if _, exists := notebookByName[t.Notebook.DisplayName]; !exists {
			notebookByName[t.Notebook.DisplayName] = t
		}
	}
	notebookNames := make([]string, 0, len(notebookByName))
	for name := range notebookByName {
		notebookNames = append(notebookNames, name)
	}
	sort.Strings(notebookNames)

	// Pre-check currently-favourited notebooks so the user sees their state.
	selectedNames, err := ui.MultiSelect(
		"Pick favourite notebooks",
		notebookNames,
		customer.FavoriteNames(),
	)
	if err != nil {
		return err
	}

	// Build working favourites list, carrying over any existing parameter
	// filters for notebooks that remained favourited.
	favourites := mergeFavorites(selectedNames, customer.Favorites)
	lastSaved := customer.Favorites

	// Reuse parsed parameter lists across drill-down iterations so re-opening
	// the same notebook doesn't re-fetch its (multi-MB) ipynb.
	paramCache := make(map[string][]fabric.Parameter)

	// Drill-down loop: let the user edit per-notebook param filters
	// until they pick "Save and exit" (or esc the menu).
	for {
		if !reflect.DeepEqual(lastSaved, favourites) {
			if err := config.SetFavorites(configPath, customerName, favourites); err != nil {
				return fmt.Errorf("save favourites: %w", err)
			}
			lastSaved = cloneFavorites(favourites)
		}

		choice, err := drillDownMenu(favourites)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				break // esc = done, already persisted
			}
			return err
		}
		if choice == menuValueSave {
			break
		}

		// User picked a notebook to edit param filters for.
		t, ok := notebookByName[choice]
		if !ok {
			fmt.Printf("Notebook %q no longer exists in workspace — skipping.\n", choice)
			continue
		}
		if err := editFavoriteParams(client, token, t.Workspace.ID, t.Notebook, favourites, paramCache); err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				continue // user esc'd the param picker — keep existing filter
			}
			return err
		}
	}

	fmt.Printf("Favourites saved for %s (%d notebook%s).\n",
		customerName, len(favourites), pluralS(len(favourites)))
	return nil
}

// cloneFavorites deep-copies a favourites slice so equality diffs
// against it aren't defeated by callers mutating the live slice.
func cloneFavorites(in []config.NotebookFavorite) []config.NotebookFavorite {
	out := make([]config.NotebookFavorite, len(in))
	for i, f := range in {
		out[i] = f
		if f.Parameters != nil {
			out[i].Parameters = append([]string(nil), f.Parameters...)
		}
	}
	return out
}

// mergeFavorites preserves existing parameter filters when the user
// re-favourites a notebook that was already in their list, and drops
// entries for notebooks that got un-checked.
func mergeFavorites(selectedNames []string, existing []config.NotebookFavorite) []config.NotebookFavorite {
	existingByName := make(map[string]config.NotebookFavorite, len(existing))
	for _, f := range existing {
		existingByName[f.Name] = f
	}
	out := make([]config.NotebookFavorite, 0, len(selectedNames))
	for _, name := range selectedNames {
		if prior, ok := existingByName[name]; ok {
			out = append(out, prior)
		} else {
			out = append(out, config.NotebookFavorite{Name: name})
		}
	}
	return out
}

// drillDownMenu shows the current favourites as rows plus a "Save and
// exit" option. The return value is either menuValueSave or the name of
// the notebook whose parameter filter the user wants to edit.
func drillDownMenu(favourites []config.NotebookFavorite) (string, error) {
	options := make([]ui.MenuOption, 0, len(favourites)+1)
	for _, f := range favourites {
		label := f.Name
		if len(f.Parameters) > 0 {
			label += dimmedHint(fmt.Sprintf("  (%d pinned)", len(f.Parameters)))
		} else {
			label += dimmedHint("  (all params)")
		}
		options = append(options, ui.MenuOption{Label: label, Value: f.Name})
	}
	options = append(options, ui.MenuOption{Label: "Save and exit", Value: menuValueSave})
	return ui.NumberMenu("Pin parameters per notebook (or save)", options)
}

// editFavoriteParams runs the parameter multi-select for one notebook
// and mutates the matching entry in `favourites` in-place. Returns
// ErrGoBack if the user escaped out of the picker. `cache` is populated
// on first fetch so re-opening the same notebook skips the round-trip.
func editFavoriteParams(client APIClient, token, workspaceID string, nb fabric.Item, favourites []config.NotebookFavorite, cache map[string][]fabric.Parameter) error {
	params, ok := cache[nb.ID]
	if !ok {
		spinner := ui.NewSpinner(fmt.Sprintf("Fetching parameters for %s...", nb.DisplayName))
		spinner.Start()
		ipynb, err := client.GetNotebookIpynb(token, workspaceID, nb.ID)
		spinner.Stop()
		if err != nil {
			return fmt.Errorf("get notebook definition: %w", err)
		}
		params, err = fabric.ParseParameters(ipynb)
		if err != nil {
			return fmt.Errorf("parse parameters: %w", err)
		}
		cache[nb.ID] = params
	}
	if len(params) == 0 {
		fmt.Printf("Notebook %s has no parameters cell — nothing to pin.\n", nb.DisplayName)
		return nil
	}

	paramNames := make([]string, len(params))
	for i, p := range params {
		paramNames[i] = p.Name
	}

	// Pre-select currently-pinned params for this notebook.
	var currentPinned []string
	for i := range favourites {
		if favourites[i].Name == nb.DisplayName {
			currentPinned = favourites[i].Parameters
			break
		}
	}

	selected, err := ui.MultiSelect(
		fmt.Sprintf("Pin parameters for %s (leave none = all)", nb.DisplayName),
		paramNames,
		currentPinned,
	)
	if err != nil {
		return err
	}

	for i := range favourites {
		if favourites[i].Name == nb.DisplayName {
			favourites[i].Parameters = selected
			break
		}
	}
	return nil
}

// dimmedHintStyle renders subtle grey-on-default text for the inline
// "(3 pinned)" / "(all params)" labels. Doesn't belong in package ui
// because it's single-use to this command; local var keeps it close
// to the caller.
var dimmedHintStyle = lipgloss.NewStyle().Foreground(ui.DimColor)

func dimmedHint(s string) string {
	return dimmedHintStyle.Render(s)
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
