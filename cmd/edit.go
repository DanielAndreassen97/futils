package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/huh"
)

const (
	editActionAddEnv        = "__add_env"
	editActionEditEnv       = "__edit_env:"
	editActionBack          = "__back"
	editActionRefOverrides  = "__ref_overrides"
	editActionSetBaseline   = "__set_baseline"
	editActionSubstitutions = "__substitutions"
	editActionExcludeTypes  = "__exclude_types"
	editActionDeployHistory = "__deploy_history"
	editActionPostDeploy    = "__post_deploy_runs"
	envActionAddWS          = "__add_ws"
	envActionRemoveWS       = "__remove_ws"
	envActionRenameAlias    = "__rename_alias"
	envActionDeleteEnv      = "__delete_env"
	envActionAddDeploy      = "__add_deploy"
	envActionRemoveDeploy   = "__remove_deploy"
)

// Edit is the top-level customer editing flow. Drills down into a
// per-customer sub-menu where the user can add / rename / drill into
// environments. Each environment in turn has its own sub-menu for
// managing its workspace list — see editEnvironmentLoop.
func Edit(configPath string) error {
	return EditWithAPI(configPath, DefaultAPI)
}

func EditWithAPI(configPath string, client APIClient) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Customers) == 0 {
		fmt.Println("No customers configured.")
		return nil
	}

	customerName, err := ui.NumberMenu("Select customer to edit", ui.MenuOptionsFromStrings(sortedCustomerNames(cfg)))
	if err != nil {
		return err
	}

	return editCustomerLoop(configPath, client, customerName)
}

// editCustomerLoop is the drill-down sub-menu that repeats until the user
// picks Back or esc's. Re-loads the customer every iteration so the
// displayed list stays in sync with whatever Add / Remove did.
func editCustomerLoop(configPath string, client APIClient, customerName string) error {
	for {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		customer, ok := cfg.Customers[customerName]
		if !ok {
			return fmt.Errorf("customer %q disappeared from config", customerName)
		}

		action, err := editCustomerMenu(customerName, customer)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}

		switch {
		case action == editActionBack:
			return nil
		case action == editActionAddEnv:
			if err := addEnvironment(configPath, client, customerName, customer); err != nil {
				if errors.Is(err, ui.ErrGoBack) {
					continue
				}
				return err
			}
		case strings.HasPrefix(action, editActionEditEnv):
			alias := strings.TrimPrefix(action, editActionEditEnv)
			if err := editEnvironmentLoop(configPath, client, customerName, alias); err != nil {
				if errors.Is(err, ui.ErrGoBack) {
					continue
				}
				return err
			}
		case action == editActionRefOverrides:
			if err := manageReferenceOverrides(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionSubstitutions:
			if err := manageSubstitutions(configPath, client, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionSetBaseline:
			if err := setBaselineEnvironment(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionExcludeTypes:
			if err := excludeItemTypes(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionPostDeploy:
			if err := editPostDeployRuns(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionDeployHistory:
			if err := setDeployHistoryPath(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		}
	}
}

// editCustomerMenu renders the per-customer menu: the current envs as
// drill-down options, plus Add-environment and Back. Each env entry's
// label includes its workspace count so the user gets a quick sense of
// where multi-workspace setup is missing.
func editCustomerMenu(customerName string, customer config.Customer) (string, error) {
	fmt.Printf("\nEditing: %s\n", customerName)
	if len(customer.Environments) == 0 {
		fmt.Println("  (no environments yet)")
	} else {
		for _, e := range customer.Environments {
			fmt.Printf("  %-12s → %d workspace%s\n", e.Alias, len(e.Workspaces), pluralS(len(e.Workspaces)))
		}
	}
	if customer.BaselineEnvironment != "" {
		fmt.Printf("  baseline environment: %s\n", customer.BaselineEnvironment)
	} else {
		fmt.Println("  baseline environment: (unset — auto-rebind disabled)")
	}
	fmt.Println()

	options := make([]ui.MenuOption, 0, len(customer.Environments)+2)
	for _, e := range customer.Environments {
		label := fmt.Sprintf("Edit %s (%d workspace%s)", e.Alias, len(e.Workspaces), pluralS(len(e.Workspaces)))
		options = append(options, ui.MenuOption{Label: label, Value: editActionEditEnv + e.Alias})
	}
	options = append(options,
		ui.MenuOption{Label: "Add environment", Value: editActionAddEnv},
		ui.MenuOption{Label: "Set baseline environment", Value: editActionSetBaseline},
		ui.MenuOption{Label: "Reference overrides", Value: editActionRefOverrides},
		ui.MenuOption{Label: "Custom substitutions (find/replace)", Value: editActionSubstitutions},
		ui.MenuOption{Label: "Exclude item types from compare", Value: editActionExcludeTypes},
		ui.MenuOption{Label: "Post-deploy runs", Value: editActionPostDeploy},
		ui.MenuOption{Label: "Set deploy-history folder", Value: editActionDeployHistory},
		ui.MenuOption{Label: "Back", Value: editActionBack},
	)
	return ui.NumberMenu("Action", options)
}

// editEnvironmentLoop is the per-env sub-menu where the user can add or
// remove workspaces, rename the alias, or delete the env entirely.
// Re-loads on every iteration so the workspace list stays current.
func editEnvironmentLoop(configPath string, client APIClient, customerName, alias string) error {
	for {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		customer, ok := cfg.Customers[customerName]
		if !ok {
			return fmt.Errorf("customer %q disappeared from config", customerName)
		}
		envIdx := findEnvIndex(customer, alias)
		if envIdx < 0 {
			return nil // env was deleted in a prior iteration — pop back up
		}
		env := customer.Environments[envIdx]

		fmt.Printf("\nEditing env: %s (%s)\n", customerName, alias)
		if len(env.Workspaces) == 0 {
			fmt.Println("  (no workspaces)")
		} else {
			for _, ws := range env.Workspaces {
				fmt.Printf("  • %s\n", ws)
			}
		}
		if len(env.Deployments) > 0 {
			fmt.Println("  Deployments:")
			for _, d := range env.Deployments {
				fmt.Printf("    %s/ → %s\n", d.Folder, d.Workspace)
			}
		}
		fmt.Println()

		options := []ui.MenuOption{
			{Label: "Add workspace", Value: envActionAddWS},
			{Label: "Remove workspace", Value: envActionRemoveWS},
			{Label: "Rename alias", Value: envActionRenameAlias},
			{Label: "Delete this environment", Value: envActionDeleteEnv},
			{Label: "Add deployment mapping", Value: envActionAddDeploy},
			{Label: "Remove deployment mapping", Value: envActionRemoveDeploy},
			{Label: "Back", Value: editActionBack},
		}
		action, err := ui.NumberMenu("Action", options)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}

		switch action {
		case editActionBack:
			return nil
		case envActionAddWS:
			err = addWorkspaceToEnv(configPath, client, customerName, alias, customer)
		case envActionRemoveWS:
			err = removeWorkspaceFromEnv(configPath, customerName, alias, customer)
		case envActionRenameAlias:
			var newAlias string
			newAlias, err = renameEnvAlias(configPath, customerName, alias, customer)
			if err == nil {
				alias = newAlias
			}
		case envActionAddDeploy:
			err = addDeploymentMapping(configPath, customerName, alias, customer)
		case envActionRemoveDeploy:
			err = removeDeploymentMapping(configPath, customerName, alias, customer)
		case envActionDeleteEnv:
			ok, derr := ui.Confirm(fmt.Sprintf("Delete env %q and all its workspaces?", alias))
			if derr != nil {
				err = derr
			} else if ok {
				customer.Environments = append(customer.Environments[:envIdx], customer.Environments[envIdx+1:]...)
				if serr := config.EditCustomer(configPath, customerName, customer); serr != nil {
					err = fmt.Errorf("save customer: %w", serr)
				} else {
					fmt.Printf("Removed environment %q\n", alias)
					return nil
				}
			}
		}
		if err != nil && !errors.Is(err, ui.ErrGoBack) {
			return err
		}
	}
}

// findEnvIndex returns the position of an env by alias, or -1 if absent.
func findEnvIndex(c config.Customer, alias string) int {
	for i, e := range c.Environments {
		if e.Alias == alias {
			return i
		}
	}
	return -1
}

// addEnvironment creates a new env with a single starter workspace.
// Additional workspaces can be added later via the env-edit sub-menu —
// keeps the create flow short for the common case of "one env, one
// workspace, see if it works".
func addEnvironment(configPath string, client APIClient, customerName string, customer config.Customer) error {
	fmt.Println(infoStyle.Render("Authenticating..."))
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	chosenName, err := pickAvailableWorkspace(client, token, customer)
	if err != nil {
		return err
	}

	// Alias prompt with validation retries. Esc sends user back to the
	// workspace picker (the caller will surface ErrGoBack one level up).
	var alias string
	for {
		alias = ""
		err := runFormStep(huh.NewInput().Title("Alias for this workspace").Value(&alias))
		if errors.Is(err, ui.ErrGoBack) {
			return ui.ErrGoBack
		}
		if err != nil {
			return err
		}
		alias = strings.TrimSpace(alias)
		if err := validateNewAlias(alias, customer.Environments); err != nil {
			fmt.Printf("  %s\n", err)
			continue
		}
		break
	}

	customer.Environments = append(customer.Environments, config.Environment{
		Alias:      alias,
		Workspaces: []string{chosenName},
	})
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Added environment %q with workspace %s\n", alias, chosenName)
	return nil
}

// addWorkspaceToEnv lets the user pick another workspace to attach to
// an existing env. Workspaces already attached to any env on this
// customer are excluded — running notebooks against a workspace via two
// different env aliases would be confusing without a real win.
func addWorkspaceToEnv(configPath string, client APIClient, customerName, alias string, customer config.Customer) error {
	fmt.Println(infoStyle.Render("Authenticating..."))
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	chosenName, err := pickAvailableWorkspace(client, token, customer)
	if err != nil {
		return err
	}

	idx := findEnvIndex(customer, alias)
	if idx < 0 {
		return fmt.Errorf("env %q not found", alias)
	}
	customer.Environments[idx].Workspaces = append(customer.Environments[idx].Workspaces, chosenName)
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Added workspace %s to env %q\n", chosenName, alias)
	return nil
}

// removeWorkspaceFromEnv lets the user drop a workspace from an env.
// Removing the last workspace leaves the env empty — run/refresh will
// surface a clear error at runtime rather than silently succeeding.
func removeWorkspaceFromEnv(configPath, customerName, alias string, customer config.Customer) error {
	idx := findEnvIndex(customer, alias)
	if idx < 0 {
		return fmt.Errorf("env %q not found", alias)
	}
	env := customer.Environments[idx]
	if len(env.Workspaces) == 0 {
		fmt.Println("No workspaces to remove.")
		return nil
	}

	options := make([]ui.MenuOption, len(env.Workspaces))
	for i, ws := range env.Workspaces {
		options[i] = ui.MenuOption{Label: ws, Value: ws}
	}
	chosen, err := ui.NumberMenu("Select workspace to remove", options)
	if err != nil {
		return err
	}

	ok, err := ui.Confirm(fmt.Sprintf("Remove workspace %q from env %q?", chosen, alias))
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("Cancelled.")
		return nil
	}

	filtered := make([]string, 0, len(env.Workspaces)-1)
	for _, ws := range env.Workspaces {
		if ws != chosen {
			filtered = append(filtered, ws)
		}
	}
	customer.Environments[idx].Workspaces = filtered
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Removed workspace %s from %q\n", chosen, alias)
	return nil
}

// renameEnvAlias updates the alias of an existing env, validating that
// the new name is non-empty and doesn't collide with another env on the
// same customer. Returns the alias that should be used for the next
// iteration of the caller's loop — either the new alias on success,
// the original oldAlias on no-op/cancel, or "" if the caller should
// exit the loop.
func renameEnvAlias(configPath, customerName, oldAlias string, customer config.Customer) (string, error) {
	idx := findEnvIndex(customer, oldAlias)
	if idx < 0 {
		return "", fmt.Errorf("env %q not found", oldAlias)
	}
	// Hoist the "others" slice — it doesn't change between retries.
	others := make([]config.Environment, 0, len(customer.Environments)-1)
	for i, e := range customer.Environments {
		if i != idx {
			others = append(others, e)
		}
	}
	var newAlias string
	for {
		newAlias = ""
		err := runFormStep(huh.NewInput().Title(fmt.Sprintf("New alias for %s", oldAlias)).Value(&newAlias))
		if errors.Is(err, ui.ErrGoBack) {
			return oldAlias, ui.ErrGoBack
		}
		if err != nil {
			return oldAlias, err
		}
		newAlias = strings.TrimSpace(newAlias)
		// Allow no-op rename (new == old) to mean "cancel, leave alone".
		if strings.EqualFold(newAlias, oldAlias) {
			return oldAlias, nil
		}
		if err := validateNewAlias(newAlias, others); err != nil {
			fmt.Printf("  %s\n", err)
			continue
		}
		break
	}
	customer.Environments[idx].Alias = newAlias
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return oldAlias, fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Renamed %s → %s\n", oldAlias, newAlias)
	return newAlias, nil
}

// pickAvailableWorkspace lists workspaces the user has access to,
// excluding ones already attached to any env on this customer, and
// shows them in a single-select menu.
func pickAvailableWorkspace(client APIClient, token string, customer config.Customer) (string, error) {
	spinner := ui.NewSpinner("Loading workspaces...")
	spinner.Start()
	workspaces, err := client.ListWorkspaces(token)
	spinner.Stop()
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}

	used := make(map[string]bool)
	for _, e := range customer.Environments {
		for _, ws := range e.Workspaces {
			used[ws] = true
		}
	}
	available := make([]fabric.Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		if !used[ws.DisplayName] {
			available = append(available, ws)
		}
	}
	if len(available) == 0 {
		// Soft-fail: not a real error, just a state where every
		// workspace is already aliased. Return ErrGoBack so the caller
		// pops the user back to the previous menu without printing a
		// red "Error:" line.
		fmt.Println("No unaliased workspaces available.")
		return "", ui.ErrGoBack
	}
	sort.Slice(available, func(i, j int) bool {
		return available[i].DisplayName < available[j].DisplayName
	})

	options := make([]ui.MenuOption, len(available))
	for i, ws := range available {
		options[i] = ui.MenuOption{Label: ws.DisplayName, Value: ws.DisplayName}
	}
	return ui.NumberMenu("Select workspace", options)
}

// validateNewAlias rejects empty / whitespace-only aliases and
// case-insensitive duplicates of an existing alias in the same customer.
// Case-insensitive because a menu with both "DEV" and "dev" is a UX trap.
func validateNewAlias(alias string, existing []config.Environment) error {
	if strings.TrimSpace(alias) == "" {
		return fmt.Errorf("alias required")
	}
	for _, e := range existing {
		if strings.EqualFold(e.Alias, alias) {
			return fmt.Errorf("alias %q already exists", alias)
		}
	}
	return nil
}

// addDeploymentMapping prompts for a repo subfolder and the workspace its items
// should deploy to (chosen from the env's configured workspaces), then appends
// a DeployMapping to the environment.
func addDeploymentMapping(configPath, customerName, alias string, customer config.Customer) error {
	idx := findEnvIndex(customer, alias)
	if idx < 0 {
		return fmt.Errorf("env %q not found", alias)
	}
	env := customer.Environments[idx]
	if len(env.Workspaces) == 0 {
		fmt.Println("Add at least one workspace to this environment before mapping a folder to it.")
		return nil
	}

	var folder string
	if err := runFormStep(huh.NewInput().Title("Repo subfolder (e.g. Backend)").Value(&folder)); err != nil {
		return err
	}
	folder = strings.Trim(strings.TrimSpace(folder), "/")
	if folder == "" {
		return fmt.Errorf("folder required")
	}

	wsOptions := make([]ui.MenuOption, len(env.Workspaces))
	for i, ws := range env.Workspaces {
		wsOptions[i] = ui.MenuOption{Label: ws, Value: ws}
	}
	workspace, err := ui.NumberMenu("Deploy this folder to which workspace?", wsOptions)
	if err != nil {
		return err
	}

	customer.Environments[idx].Deployments = append(customer.Environments[idx].Deployments,
		config.DeployMapping{Folder: folder, Workspace: workspace})
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Mapped %s/ → %s in env %q\n", folder, workspace, alias)
	return nil
}

// setBaseline sets (or, with an empty alias, clears) the customer's baseline
// environment. Pure — the caller persists the result.
func setBaseline(c config.Customer, alias string) config.Customer {
	c.BaselineEnvironment = alias
	return c
}

// setBaselineEnvironment lets the user choose which of the customer's
// environments is the baseline (the env the git GUIDs belong to — the source
// for GUID→name resolution during auto-rebind), or clear it. The picker offers
// only the customer's own aliases, so the saved value is always a real env.
func setBaselineEnvironment(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	if len(customer.Environments) == 0 {
		fmt.Println("Add an environment first, then set which one is the baseline.")
		return nil
	}
	options := make([]ui.MenuOption, 0, len(customer.Environments)+1)
	for _, e := range customer.Environments {
		label := e.Alias
		if e.Alias == customer.BaselineEnvironment {
			label += " (current)"
		}
		options = append(options, ui.MenuOption{Label: label, Value: e.Alias})
	}
	options = append(options, ui.MenuOption{Label: "Clear (disable auto-rebind)", Value: ""})
	chosen, err := ui.NumberMenu("Which environment do the git GUIDs belong to?", options)
	if err != nil {
		return err
	}
	customer = setBaseline(customer, chosen)
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	if chosen == "" {
		fmt.Println("Baseline environment cleared (auto-rebind disabled).")
	} else {
		fmt.Printf("Baseline environment set to %q.\n", chosen)
	}
	return nil
}

// removeOverride returns a copy of c with the ReferenceOverride for sourceGUID removed.
func removeOverride(c config.Customer, sourceGUID string) config.Customer {
	next := c.ReferenceOverrides[:0]
	for _, o := range c.ReferenceOverrides {
		if o.SourceGUID != sourceGUID {
			next = append(next, o)
		}
	}
	c.ReferenceOverrides = next
	return c
}

// removeIgnored returns a copy of c with the given guid removed from IgnoredReferences.
func removeIgnored(c config.Customer, guid string) config.Customer {
	next := c.IgnoredReferences[:0]
	for _, g := range c.IgnoredReferences {
		if g != guid {
			next = append(next, g)
		}
	}
	c.IgnoredReferences = next
	return c
}

// manageReferenceOverrides lists the customer's saved reference overrides and
// ignored references and lets the user remove them. Adding overrides happens
// inline during a dry-run (where futils knows the baseline GUIDs); this section
// is for review and cleanup.
func manageReferenceOverrides(configPath, customerName string) error {
	for {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		customer, ok := cfg.Customers[customerName]
		if !ok {
			return fmt.Errorf("customer %q disappeared from config", customerName)
		}
		fmt.Printf("\nReference overrides for %s\n", customerName)
		if len(customer.ReferenceOverrides) == 0 && len(customer.IgnoredReferences) == 0 {
			fmt.Println("  (none — add them inline after a dry-run)")
		}
		var options []ui.MenuOption
		for _, o := range customer.ReferenceOverrides {
			label := fmt.Sprintf("Remove override: %s → %s %q", shortGUID(o.SourceGUID), o.ItemType, o.ItemName)
			options = append(options, ui.MenuOption{Label: label, Value: "ovr:" + o.SourceGUID})
		}
		for _, g := range customer.IgnoredReferences {
			options = append(options, ui.MenuOption{Label: "Un-ignore: " + shortGUID(g), Value: "ign:" + g})
		}
		options = append(options, ui.MenuOption{Label: "Back", Value: editActionBack})
		choice, err := ui.NumberMenu("Action", options)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}
		switch {
		case choice == editActionBack:
			return nil
		case strings.HasPrefix(choice, "ovr:"):
			customer = removeOverride(customer, strings.TrimPrefix(choice, "ovr:"))
		case strings.HasPrefix(choice, "ign:"):
			customer = removeIgnored(customer, strings.TrimPrefix(choice, "ign:"))
		}
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			return fmt.Errorf("save customer: %w", err)
		}
	}
}

// addSubstitution returns a copy of c with s appended to Substitutions.
// Copy-on-write: never mutates the caller's backing array.
func addSubstitution(c config.Customer, s config.Substitution) config.Customer {
	c.Substitutions = append(append([]config.Substitution{}, c.Substitutions...), s)
	return c
}

// removeSubstitution returns a copy of c with the substitution at index i removed.
// An out-of-range index is a no-op. Copy-on-write.
func removeSubstitution(c config.Customer, i int) config.Customer {
	if i < 0 || i >= len(c.Substitutions) {
		return c
	}
	next := append([]config.Substitution{}, c.Substitutions[:i]...)
	next = append(next, c.Substitutions[i+1:]...)
	c.Substitutions = next
	return c
}

// manageSubstitutions lists the customer's custom find→replace rules and lets
// the user add or remove them. Adding: type the find string, then choose a
// literal replacement or a target item resolved by name (pick workspace → item
// → attribute). Follows the same loop conventions as manageReferenceOverrides.
func manageSubstitutions(configPath string, client APIClient, customerName string) error {
	for {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		customer, ok := cfg.Customers[customerName]
		if !ok {
			return fmt.Errorf("customer %q disappeared from config", customerName)
		}
		fmt.Printf("\nCustom substitutions for %s\n", customerName)
		if len(customer.Substitutions) == 0 {
			fmt.Println("  (none)")
		}
		var options []ui.MenuOption
		for i, s := range customer.Substitutions {
			repl := s.Literal
			if s.TargetType != "" {
				repl = fmt.Sprintf("%s %q.%s", s.TargetType, s.TargetName, attrOrID(s.Attr))
			}
			options = append(options, ui.MenuOption{
				Label: fmt.Sprintf("Remove: %q → %s", s.FindValue, repl),
				Value: fmt.Sprintf("rm:%d", i),
			})
		}
		options = append(options,
			ui.MenuOption{Label: "Add substitution", Value: "add"},
			ui.MenuOption{Label: "Back", Value: editActionBack},
		)
		choice, err := ui.NumberMenu("Action", options)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}
		switch {
		case choice == editActionBack:
			return nil
		case choice == "add":
			sub, aerr := promptSubstitution(client, customerName)
			if aerr != nil {
				if errors.Is(aerr, ui.ErrGoBack) {
					continue
				}
				return aerr
			}
			customer = addSubstitution(customer, sub)
		case strings.HasPrefix(choice, "rm:"):
			idx, _ := strconv.Atoi(strings.TrimPrefix(choice, "rm:"))
			customer = removeSubstitution(customer, idx)
		}
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			return fmt.Errorf("save customer: %w", err)
		}
	}
}

// attrOrID returns the attr label, defaulting to "id".
func attrOrID(attr string) string {
	if attr == "" {
		return "id"
	}
	return attr
}

// promptSubstitution gathers one substitution: a find string, then either a
// literal replacement or a target item (pick workspace → item) + attribute.
func promptSubstitution(client APIClient, customerName string) (config.Substitution, error) {
	var find string
	if err := runFormStep(huh.NewInput().Title("Find value (the string to replace)").Value(&find)); err != nil {
		return config.Substitution{}, err
	}
	find = strings.TrimSpace(find)
	if find == "" {
		return config.Substitution{}, fmt.Errorf("find value required")
	}
	kind, err := ui.NumberMenu("Replace with", []ui.MenuOption{
		{Label: "A target item resolved by name (id / sql endpoint)", Value: "target"},
		{Label: "A literal value", Value: "literal"},
	})
	if err != nil {
		return config.Substitution{}, err
	}
	if kind == "literal" {
		var lit string
		if err := runFormStep(huh.NewInput().Title("Literal replacement value").Value(&lit)); err != nil {
			return config.Substitution{}, err
		}
		return config.Substitution{FindValue: find, Literal: lit}, nil
	}
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return config.Substitution{}, fmt.Errorf("authentication failed: %w", err)
	}
	itemType, itemName, err := pickTargetItem(client, token, "")
	if err != nil {
		return config.Substitution{}, err
	}
	attr, err := ui.NumberMenu("Which attribute of the target item?", []ui.MenuOption{
		{Label: "Item GUID (id)", Value: "id"},
		{Label: "SQL endpoint host", Value: "sqlendpoint"},
		{Label: "SQL endpoint database id", Value: "sqlendpointid"},
	})
	if err != nil {
		return config.Substitution{}, err
	}
	return config.Substitution{FindValue: find, TargetType: itemType, TargetName: itemName, Attr: attr}, nil
}

// removeDeploymentMapping lets the user drop a folder→workspace mapping from an
// environment.
func removeDeploymentMapping(configPath, customerName, alias string, customer config.Customer) error {
	idx := findEnvIndex(customer, alias)
	if idx < 0 {
		return fmt.Errorf("env %q not found", alias)
	}
	env := customer.Environments[idx]
	if len(env.Deployments) == 0 {
		fmt.Println("No deployment mappings to remove.")
		return nil
	}

	options := make([]ui.MenuOption, len(env.Deployments))
	for i, d := range env.Deployments {
		options[i] = ui.MenuOption{Label: fmt.Sprintf("%s/ → %s", d.Folder, d.Workspace), Value: fmt.Sprintf("%d", i)}
	}
	chosen, err := ui.NumberMenu("Select mapping to remove", options)
	if err != nil {
		return err
	}

	var keep []config.DeployMapping
	for i, d := range env.Deployments {
		if fmt.Sprintf("%d", i) != chosen {
			keep = append(keep, d)
		}
	}
	customer.Environments[idx].Deployments = keep
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Removed mapping from env %q\n", alias)
	return nil
}

// mergeSorted returns the sorted, de-duplicated union of two string slices.
func mergeSorted(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// excludeItemTypes lets the user pick which item types to skip when comparing.
// The picker is populated from the item types actually present in the customer's
// repo (a local scan). Selected = excluded; default nothing selected = compare
// everything. Stored as Customer.ExcludedItemTypes.
func excludeItemTypes(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	if customer.RepoPath == "" {
		fmt.Println(infoStyle.Render("Set a repo path first (this customer has none) — can't list item types."))
		return ui.ErrGoBack
	}
	types, err := deploy.RepoItemTypes(customer.RepoPath)
	if err != nil {
		return fmt.Errorf("scan repo for item types: %w", err)
	}
	// Offer the union of what's in the repo now AND what's already excluded:
	// MultiSelect only returns checked options that appear in the list, so a
	// previously-excluded type missing from the current scan (folder renamed,
	// not yet pulled, or temporarily removed) would be silently dropped on
	// confirm. Keeping it in the list — pre-checked — preserves the choice.
	options := mergeSorted(types, customer.ExcludedItemTypes)
	if len(options) == 0 {
		fmt.Println(infoStyle.Render("No Fabric items found under the repo path."))
		return ui.ErrGoBack
	}

	chosen, err := ui.MultiSelect("Select item types to EXCLUDE from compare (none = compare all)", options, customer.ExcludedItemTypes)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		customer.ExcludedItemTypes = nil
	} else {
		customer.ExcludedItemTypes = chosen
	}
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save excluded item types: %w", err)
	}
	fmt.Println(infoStyle.Render("Saved excluded item types."))
	return nil
}

// editPostDeployRuns lets the user pick which notebooks futils offers to run
// after each deploy. Only notebooks actually deployed (created/updated) in a
// given run are offered at deploy time; this list is the superset. The saved
// JSON order is the run order — reorder in the config file if needed.
func editPostDeployRuns(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	if customer.RepoPath == "" {
		fmt.Println(infoStyle.Render("Set a repo path first (this customer has none) — can't list notebooks."))
		return ui.ErrGoBack
	}
	names, err := deploy.RepoItemNames(customer.RepoPath, "Notebook")
	if err != nil {
		return fmt.Errorf("scan repo for notebooks: %w", err)
	}
	// Union of repo notebooks and already-registered names, so a registered
	// notebook missing from the current scan isn't silently dropped on save.
	options := mergeSorted(names, customer.PostDeployRuns)
	if len(options) == 0 {
		fmt.Println(infoStyle.Render("No notebooks found under the repo path."))
		return ui.ErrGoBack
	}

	chosen, err := ui.MultiSelect("Select notebooks to offer as post-deploy runs (only deployed ones are offered per run)", options, customer.PostDeployRuns)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		customer.PostDeployRuns = nil
	} else {
		customer.PostDeployRuns = chosen
	}
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save post-deploy runs: %w", err)
	}
	fmt.Println(infoStyle.Render("Saved post-deploy runs. Run order = list order in the config file."))
	return nil
}

// setDeployHistoryPath sets the repo-relative folder where deploy reports are
// written after each real deploy. Empty input turns history off. Pre-filled
// with the current value.
func setDeployHistoryPath(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	path := customer.DeployHistoryPath
	if err := runFormStep(huh.NewInput().
		Title("Deploy-history folder (relative to repo root; empty = off)").
		Value(&path)); err != nil {
		return err
	}
	customer.DeployHistoryPath = strings.TrimSpace(path)
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save deploy-history path: %w", err)
	}
	if customer.DeployHistoryPath == "" {
		fmt.Println(infoStyle.Render("Deploy-history saving turned off."))
	} else {
		fmt.Println(infoStyle.Render("Saved deploy-history folder: " + customer.DeployHistoryPath))
	}
	return nil
}
