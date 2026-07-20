package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/huh"
)

const (
	editActionAddEnv        = "__add_env"
	editActionEditEnv       = "__edit_env:"
	editActionBack          = "__back"
	editActionRefOverrides  = "__ref_overrides"
	editActionSetBaseline   = "__set_baseline"
	editActionSetRepo       = "__set_repo"
	editActionSubstitutions = "__substitutions"
	editActionExcludeTypes  = "__exclude_types"
	editActionDeployHistory = "__deploy_history"
	editActionSetBranch     = "__deploy_branch"
	editActionSchedules     = "__schedules"
	editActionBulkBackend   = "__bulk_backend"
	editActionPostDeploy    = "__post_deploy_runs"
	editActionFavorites     = "__favorites"
	envActionAddWS          = "__add_ws"
	envActionRemoveWS       = "__remove_ws"
	envActionRenameAlias    = "__rename_alias"
	envActionDeleteEnv      = "__delete_env"
	envActionAddDeploy      = "__add_deploy"
	envActionEditDeploy     = "__edit_deploy:"
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
		case action == editActionSetRepo:
			if err := setRepoPath(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
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
		case action == editActionSetBranch:
			if err := setDeployBranch(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionDeployHistory:
			if err := setDeployHistoryPath(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionSchedules:
			if err := toggleSchedules(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionBulkBackend:
			if err := toggleBulkBackend(configPath, customerName); err != nil && !errors.Is(err, ui.ErrGoBack) {
				return err
			}
		case action == editActionFavorites:
			if err := favoritesForCustomer(configPath, client, customerName, customer); err != nil && !errors.Is(err, ui.ErrGoBack) {
				// The navigation sentinels must keep unwinding — swallowing them
				// here would trap q/m inside the edit-customer loop.
				if errors.Is(err, ui.ErrQuit) || errors.Is(err, ui.ErrGoHome) {
					return err
				}
				// Soft-fail: e.g. "no environments configured" when the customer
				// has none yet. A plain return would eject the user from the
				// edit-customer menu they'd need to add one from — print and
				// stay in the loop instead, same as the other in-loop notices
				// below (repo path, deploy failures, etc.) use warningStyle for.
				fmt.Println(warningStyle.Render(err.Error()))
			}
		}
	}
}

// editCustomerMenu renders the per-customer menu: the current envs as
// drill-down options, plus Add-environment and Back. Each env entry's
// label includes its workspace count so the user gets a quick sense of
// where multi-workspace setup is missing.
// autoBranchBadge memoizes the resolved "AUTO → <branch>" badge per repo path
// for the session — see the resolve site in editCustomerMenu.
var autoBranchBadge = map[string]string{}

func editCustomerMenu(customerName string, customer config.Customer) (string, error) {
	// The menu below now conveys the env list (as "Edit <alias> (N workspace)"
	// rows under the Environments header) and baseline status (via the badge),
	// so the pre-print is just the customer title for context.
	fmt.Printf("\nEditing: %s\n", customerName)

	// Surface the configured baseline's name right on the row — not just
	// whether one is set — so the user can confirm which env auto-rebind
	// treats as the source of truth without pressing ? for the Info box.
	baselineBadge := "MUST SET"
	if customer.BaselineEnvironment != "" {
		baselineBadge = customer.BaselineEnvironment
	}
	// Same at-a-glance treatment as the baseline badge: show which repo is the
	// primary without having to enter the picker.
	repoBadge := "NOT SET"
	if customer.RepoPath != "" {
		repoBadge = filepath.Base(customer.RepoPath)
	}
	// AUTO alone answers "what happens", not "which branch" — when a repo is
	// configured, resolve the default so the badge shows what a deploy would
	// actually read. Memoized per repo path: the menu re-renders after every
	// submenu round-trip, and each resolve is a handful of git subprocesses
	// for an answer that doesn't change within a session.
	branchBadge := "AUTO"
	if customer.DeployBranch != "" {
		branchBadge = customer.DeployBranch
	} else if customer.RepoPath != "" {
		if b, ok := autoBranchBadge[customer.RepoPath]; ok {
			branchBadge = b
		} else if src, err := deploy.NewSource(customer.RepoPath); err == nil {
			branchBadge = "AUTO → " + strings.TrimPrefix(src.Ref(), "origin/")
			autoBranchBadge[customer.RepoPath] = branchBadge
		} else {
			autoBranchBadge[customer.RepoPath] = branchBadge // negative-cache the failure too
		}
	}
	schedulesBadge := "DEPLOYED"
	if customer.SkipSchedules {
		schedulesBadge = "KEPT IN TARGET"
	}
	bulkBadge := "OFF"
	if customer.UseBulkDeploy {
		bulkBadge = "ON (preview)"
	}

	// Grouped under section headers so the menu scans as three concerns:
	// the environments themselves, one-time deploy configuration, and what
	// runs after a deploy. Labels are terse — the footer Description carries
	// the detail — so rows stay short and the headers do the explaining.
	options := make([]ui.MenuOption, 0, len(customer.Environments)+13)
	options = append(options, ui.MenuOption{Label: "Environments", IsHeader: true})
	for _, e := range customer.Environments {
		label := fmt.Sprintf("Edit %s (%d workspace%s", e.Alias, len(e.Workspaces), pluralS(len(e.Workspaces)))
		if n := len(e.Deployments); n > 0 {
			label += fmt.Sprintf(", %d mapping%s", n, pluralS(n))
		}
		label += ")"
		options = append(options, ui.MenuOption{Label: label, Value: editActionEditEnv + e.Alias})
	}
	options = append(options,
		ui.MenuOption{
			Label:       "Add environment",
			Value:       editActionAddEnv,
			Description: "Create a named environment (DEV, TEST, PROD…) and pick the workspaces it spans.",
			Info:        "An environment is a named set of Fabric workspaces (e.g. DEV = 'DW - DEV - Config' + 'DW - DEV - SemMod'). Deploys target an environment, and auto-rebind resolves references against its full workspace set — so include reference-only workspaces too (e.g. a Data workspace you never deploy to directly).",
		},

		ui.MenuOption{Label: "Deploy setup", IsHeader: true},
		ui.MenuOption{
			Label:       "Primary repo path",
			Value:       editActionSetRepo,
			Badge:       repoBadge,
			Description: "The customer's main Fabric git repo. Deployment mappings are edited under each environment above, not here.",
			Info:        "futils reads your Fabric items from this repo's origin/<default-branch> (never the working tree) when it compares and deploys. Setting it here also lets the Exclude-item-types and Post-deploy pickers scan the repo right away — otherwise the path is only captured the first time you run a deploy, and those pickers can't work until then. Folder→workspace deployment mappings — including ones living in other repos — are managed per environment: Edit <env> → Add/Remove deployment mapping.",
		},
		ui.MenuOption{
			Label:       "Deploy branch",
			Value:       editActionSetBranch,
			Badge:       branchBadge,
			Description: "Which origin branch deploys read from. AUTO = the remote's default branch.",
			Info:        "Deploys always read from origin/<branch>, never your working tree. AUTO resolves the remote's default branch (origin/HEAD, falling back to main, then master) — right for almost everyone. Pin a branch here to deploy from something else: origin/dev, a release branch, or a default branch with a different name. The pin applies to the primary repo; per-mapping repos keep auto-detection.",
		},
		ui.MenuOption{
			Label:       "Baseline environment",
			Value:       editActionSetBaseline,
			Description: "Which environment the git GUIDs belong to (usually DEV). Required for auto-rebind.",
			Info:        "Baseline is the environment your repo represents. futils reads the GUIDs in git as baseline GUIDs, resolves them by name, and swaps to the target environment's GUIDs on deploy. Without a baseline, auto-rebind is off and references deploy unchanged.",
			Badge:       baselineBadge,
		},
		ui.MenuOption{
			Label:       "Exclude item types",
			Value:       editActionExcludeTypes,
			Description: "Item types to skip when comparing/deploying.",
			Info:        "Some item types round-trip through Fabric with cosmetic reformatting that shows as a phantom 'Changed' on every deploy, and some you simply never deploy from this repo. List those types here and futils leaves them out of both the compare and the deploy.",
		},
		ui.MenuOption{
			Label:       "Schedules",
			Value:       editActionSchedules,
			Badge:       schedulesBadge,
			Description: "Whether .schedules parts deploy with pipelines, or stay managed per environment.",
			Info:        "Fabric git-sync writes pipeline schedules to a .schedules part. Deploying it overwrites the target's schedules with the source's — including deleting them when the source has none. Set KEPT IN TARGET to leave every environment's schedules alone: futils then excludes .schedules from both the compare and the deploy payload (the definition API treats the part as optional, so the target keeps what it has).",
		},
		ui.MenuOption{
			Label:       "Bulk-import backend",
			Value:       editActionBulkBackend,
			Badge:       bulkBadge,
			Description: "Deploy with the bulk-import backend (PREVIEW) instead of the per-item one.",
			Info:        "The bulk-import backend publishes many items in one API call — a big rate-limit win on large deploys — but runs on a Fabric beta API. When ON, deploys use it without asking; when OFF, the stable per-item backend is used silently. This replaces the old per-deploy question.",
		},
		ui.MenuOption{
			Label:       "Reference overrides",
			Value:       editActionRefOverrides,
			Description: "Manual GUID→name overrides for references auto-rebind can't resolve.",
			Info:        "Auto-rebind resolves cross-environment references by matching item names between your baseline and the target. When one can't be resolved by name — an unknown GUID, a connection carrying no name, or an ambiguous match — register an override here that maps the source GUID to a target item by name explicitly. You can also un-ignore references you previously chose to skip.",
		},
		ui.MenuOption{
			Label:       "Custom substitutions",
			Value:       editActionSubstitutions,
			Description: "Your own find/replace rules, resolved by name in the target or as a literal.",
			Info:        "Find/replace rules applied to item content before deploy — the futils replacement for parameter.yml's find_replace. Each rule finds a value and swaps it for either a literal or an item resolved by name in the target environment (its id, SQL endpoint host, or endpoint id). Use it for references auto-rebind doesn't cover on its own.",
		},

		ui.MenuOption{Label: "After deploy", IsHeader: true},
		ui.MenuOption{
			Label:       "Post-deploy runs",
			Value:       editActionPostDeploy,
			Description: "Notebooks to run after a successful deploy (only the ones deployed that run).",
			Info:        "After a successful deploy, futils offers to run the notebooks you register here — but only the ones actually created or updated in that run — sequentially, stopping at the first failure. Handy for re-seeding config or running smoke tests in the target without doing it by hand.",
		},
		ui.MenuOption{
			Label:       "Deploy-history folder",
			Value:       editActionDeployHistory,
			Description: "Repo-relative folder where a timestamped HTML report is written per deploy.",
			Info:        "A repo-relative folder where futils writes a timestamped, self-contained HTML report after each real deploy — what was compared, deployed, rebound, and any post-deploy runs. The saved path is clickable in the terminal. Leave empty to turn history off.",
		},

		ui.MenuOption{Label: "Notebooks", IsHeader: true},
		ui.MenuOption{
			Label:       "Favourites",
			Value:       editActionFavorites,
			Description: "Pin notebooks (and their preferred parameters) for quicker run/refresh.",
			Info:        "Pin the notebooks you run most, optionally with their preferred parameters, so `futils run` and `futils refresh` show a short curated list first instead of every notebook in the workspace.",
		},

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

		// One clear head for the screen (design alt C): full-width accent rule
		// with the env name in bold, counts beneath, workspaces as one wrapped
		// bullet line — replaces the old stacked "Editing env:" + bullet list.
		fmt.Println()
		fmt.Println(contextBanner(alias, fmt.Sprintf("%s · %d workspace%s · %d mapping%s",
			customerName, len(env.Workspaces), pluralS(len(env.Workspaces)), len(env.Deployments), pluralS(len(env.Deployments)))))
		if len(env.Workspaces) == 0 {
			fmt.Println(wrapIndented("(no workspaces)", 3))
		} else {
			fmt.Println(wrapIndented("• "+strings.Join(env.Workspaces, "   • "), 3))
		}
		fmt.Println()

		options := []ui.MenuOption{
			{
				Label:       "Add workspace",
				Value:       envActionAddWS,
				Description: "Attach a Fabric workspace to this environment.",
				Info:        "An environment is a named set of Fabric workspaces (e.g. DEV = 'DW - DEV - Config' + 'DW - DEV - SemMod'). Auto-rebind enumerates every workspace here to build the name index it resolves references against, so add all workspaces this env spans — including reference-only ones you don't deploy to directly.",
			},
			{
				Label:       "Remove workspace",
				Value:       envActionRemoveWS,
				Description: "Detach a workspace from this environment.",
			},
			{
				Label:       "Rename alias",
				Value:       envActionRenameAlias,
				Description: "Change this environment's alias (e.g. DEV, TEST, PROD).",
			},
			{
				Label:       "Delete this environment",
				Value:       envActionDeleteEnv,
				Description: "Remove this environment and its deployment mappings.",
			},
		}
		// Each mapping is its own selectable row — enter opens its actions
		// (change workspace, change baseline, remove). The list IS the menu,
		// so there's no separate remove-mapping action or pre-printed summary.
		options = append(options, ui.MenuOption{Label: "Deployment mappings", IsHeader: true})
		for i, d := range env.Deployments {
			options = append(options, ui.MenuOption{
				Label:       fmt.Sprintf("%s → %s%s", mappingLabel(d.Folder, d.Repo), d.Workspace, baselineSuffix(d)),
				Value:       envActionEditDeploy + strconv.Itoa(i),
				Description: "Enter for actions: change workspace, change baseline workspace, or remove.",
			})
		}
		options = append(options,
			ui.MenuOption{
				Label:       "Add deployment mapping",
				Value:       envActionAddDeploy,
				Description: "Map a repo folder to a target workspace in this environment.",
				Info:        "A deployment mapping says 'deploy this repo folder to this workspace' for this environment — e.g. FabricBackEnd/ → 'DW - DEV - Config'. You can point a folder at a second git repo, so a customer with separate backend and frontend repos deploys both in one run. A folder that is the whole repo maps as an empty folder.",
			},
			ui.MenuOption{Label: "Back", Value: editActionBack},
		)
		action, err := ui.NumberMenu("Action", options)
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				return nil
			}
			return err
		}

		if strings.HasPrefix(action, envActionEditDeploy) {
			if i, aerr := strconv.Atoi(strings.TrimPrefix(action, envActionEditDeploy)); aerr == nil {
				if merr := editMappingActions(configPath, client, customerName, customer, alias, envIdx, i); merr != nil && !errors.Is(merr, ui.ErrGoBack) {
					return merr
				}
			}
			continue
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
			err = addDeploymentMapping(configPath, client, customerName, alias, customer)
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
	// Name first — "Select workspaces that belong to DEV" reads naturally,
	// the reverse (pick a workspace, then invent a name for it) doesn't.
	var alias string
	for {
		alias = ""
		err := runFormStep(huh.NewInput().Title("Environment name (e.g. DEV)").Value(&alias))
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

	fmt.Println(infoStyle.Render("Authenticating..."))
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	available, err := availableWorkspaceNames(client, token, customer)
	if err != nil {
		return err
	}
	chosen, err := ui.MultiSelect(fmt.Sprintf("Select workspaces that belong to %s", alias), available, nil)
	if err != nil {
		return err
	}
	if len(chosen) == 0 {
		fmt.Println(warningStyle.Render("No workspaces selected — environment not added."))
		return nil
	}

	customer.Environments = append(customer.Environments, config.Environment{
		Alias:      alias,
		Workspaces: chosen,
	})
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Added environment %q with %d workspace%s: %s\n", alias, len(chosen), pluralS(len(chosen)), strings.Join(chosen, ", "))
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
	available, err := availableWorkspaceNames(client, token, customer)
	if err != nil {
		return "", err
	}
	options := make([]ui.MenuOption, len(available))
	for i, name := range available {
		options[i] = ui.MenuOption{Label: name, Value: name}
	}
	return ui.NumberMenu("Select workspace", options)
}

// availableWorkspaceNames lists (sorted) workspaces the user can see that are
// not yet attached to any of this customer's environments.
func availableWorkspaceNames(client APIClient, token string, customer config.Customer) ([]string, error) {
	spinner := ui.NewSpinner("Loading workspaces...")
	spinner.Start()
	workspaces, err := client.ListWorkspaces(token)
	spinner.Stop()
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}

	used := make(map[string]bool)
	for _, e := range customer.Environments {
		for _, ws := range e.Workspaces {
			used[ws] = true
		}
	}
	available := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		if !used[ws.DisplayName] {
			available = append(available, ws.DisplayName)
		}
	}
	if len(available) == 0 {
		// Soft-fail: not a real error, just a state where every
		// workspace is already aliased. Return ErrGoBack so the caller
		// pops the user back to the previous menu without printing a
		// red "Error:" line.
		fmt.Println("No unaliased workspaces available.")
		return nil, ui.ErrGoBack
	}
	sort.Strings(available)
	return available, nil
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
// a DeployMapping to the environment. An optional dedicated baseline workspace
// isolates the mapping's reference resolution from the baseline environment.
func addDeploymentMapping(configPath string, client APIClient, customerName, alias string, customer config.Customer) error {
	idx := findEnvIndex(customer, alias)
	if idx < 0 {
		return fmt.Errorf("env %q not found", alias)
	}
	env := customer.Environments[idx]
	if len(env.Workspaces) == 0 {
		fmt.Println("Add at least one workspace to this environment before mapping a folder to it.")
		return nil
	}

	// Which repo does this folder live in? Default is the customer's primary
	// repo; "Another repo…" lets a customer deploy from a second git repo.
	repoChoice := ""
	if customer.RepoPath != "" {
		const another = "__another_repo"
		opts := []ui.MenuOption{
			{Label: fmt.Sprintf("Primary repo (%s)", customer.RepoPath), Value: ""},
			{Label: "Another repo…", Value: another},
		}
		chosen, err := ui.NumberMenu("Which repo is this folder in?", opts)
		if err != nil {
			return err
		}
		if chosen == another {
			startDir, _ := os.UserHomeDir()
			picked, perr := ui.PickDirectory("Other Fabric git repo", startDir)
			if perr != nil {
				return perr
			}
			// Mapping repos keep default-branch auto-detection — the customer
			// pin applies to the primary repo only (see DeployWithAPI).
			src, serr := deploy.NewSource(picked)
			if serr != nil {
				return fmt.Errorf("not a usable git repo: %w", serr)
			}
			repoChoice = src.Repo()
		}
	}

	var folder string
	if err := runFormStep(huh.NewInput().Title("Repo subfolder (e.g. Backend — leave empty to deploy the whole repo)").Value(&folder)); err != nil {
		return err
	}
	// Empty is valid: a folder that is the whole repo maps as an empty folder.
	folder = strings.Trim(strings.TrimSpace(folder), "/")

	wsOptions := make([]ui.MenuOption, len(env.Workspaces))
	for i, ws := range env.Workspaces {
		wsOptions[i] = ui.MenuOption{Label: ws, Value: ws}
	}
	workspace, err := ui.NumberMenu("Deploy this folder to which workspace?", wsOptions)
	if err != nil {
		return err
	}

	for _, d := range env.Deployments {
		if d.Folder == folder && d.Repo == repoChoice && strings.EqualFold(d.Workspace, workspace) {
			fmt.Println(warningStyle.Render(fmt.Sprintf("Mapping %s → %s already exists in env %q — nothing added.", mappingLabel(folder, repoChoice), workspace, alias)))
			return nil
		}
	}

	baselineWS, err := pickMappingBaseline(client, customerName, customer)
	if err != nil {
		return err
	}

	m := config.DeployMapping{Folder: folder, Workspace: workspace, Repo: repoChoice, BaselineWorkspace: baselineWS}
	customer.Environments[idx].Deployments = append(customer.Environments[idx].Deployments, m)
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	fmt.Printf("Mapped %s → %s in env %q%s\n", mappingLabel(folder, repoChoice), workspace, alias, baselineSuffix(m))
	return nil
}

// pickMappingBaseline asks whether the new mapping inherits the customer's
// baseline environment (the default, returned as "") or resolves its references
// against one dedicated baseline workspace — the multi-repo case where e.g. a
// frontend repo's baked GUIDs live in a frontend workspace the baseline env
// doesn't (and shouldn't) span.
func pickMappingBaseline(client APIClient, customerName string, customer config.Customer) (string, error) {
	const own = "__own_baseline"
	opts := []ui.MenuOption{
		{
			Label:       "Inherit from baseline environment (default)",
			Value:       "",
			Description: "References resolve against the customer's baseline environment — right for folders in the primary repo.",
		},
		{
			Label:       "Dedicated baseline workspace…",
			Value:       own,
			Description: "Isolate this mapping: its baked GUIDs resolve against ONE chosen workspace, and only into the mapping's own target workspace.",
			Info:        "Use this when the mapping's repo was developed against a workspace outside the baseline environment — e.g. a frontend repo with its own DEV workspace. Isolation also prevents 'ambiguous' matches when frontend and backend workspaces contain same-named items.",
		},
	}
	choice, err := ui.NumberMenu("Baseline for this mapping?", opts)
	if err != nil {
		return "", err
	}
	if choice != own {
		return "", nil
	}
	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return "", err
	}
	spinner := ui.NewSpinner("Loading workspaces...")
	spinner.Start()
	workspaces, err := client.ListWorkspaces(token)
	spinner.Stop()
	if err != nil {
		return "", fmt.Errorf("list workspaces: %w", err)
	}
	wsOpts := make([]ui.FilterOption, len(workspaces))
	for i, w := range workspaces {
		wsOpts[i] = ui.FilterOption{Label: w.DisplayName, Value: w.DisplayName}
	}
	return ui.FilterMenu("Baseline workspace for this mapping", wsOpts, ui.DefaultFilterRowRenderer)
}

// setBaseline sets (or, with an empty alias, clears) the customer's baseline
// environment. Pure — the caller persists the result.
func setBaseline(c config.Customer, alias string) config.Customer {
	c.BaselineEnvironment = alias
	return c
}

// editMappingActions is the per-mapping action menu opened from an
// environment's mapping row: repoint the folder at another workspace (with
// the cross-env guardrail), switch between inherited and dedicated baseline,
// or remove the mapping.
func editMappingActions(configPath string, client APIClient, customerName string, customer config.Customer, alias string, envIdx, mIdx int) error {
	if envIdx < 0 || envIdx >= len(customer.Environments) || mIdx < 0 || mIdx >= len(customer.Environments[envIdx].Deployments) {
		return nil
	}
	m := customer.Environments[envIdx].Deployments[mIdx]
	act, err := ui.NumberMenu(
		fmt.Sprintf("%s: %s → %s", alias, mappingLabel(m.Folder, m.Repo), m.Workspace),
		[]ui.MenuOption{
			{Label: "Change workspace", Value: "workspace", Description: "Point this folder at a different workspace (env " + alias + "'s own listed first)."},
			{Label: "Change baseline workspace", Value: "baseline", Description: "Inherit the baseline environment, or isolate this mapping to one dedicated baseline workspace."},
			{Label: "Remove mapping", Value: "remove", Description: "Deletes only the mapping — items and workspaces are untouched."},
			{Label: "Back", Value: "back"},
		})
	if err != nil {
		return err
	}
	switch act {
	case "workspace":
		token, terr := client.GetAccessToken(customerName)
		if terr != nil {
			return terr
		}
		spinner := ui.NewSpinner("Loading workspaces...")
		spinner.Start()
		workspaces, werr := client.ListWorkspaces(token)
		spinner.Stop()
		if werr != nil {
			return fmt.Errorf("list workspaces: %w", werr)
		}
		opts, wsEnv := mappingWorkspaceOptions(workspaces, customer, alias)
		for {
			chosen, perr := ui.FilterMenu(fmt.Sprintf("Map %s → which workspace?", mappingLabel(m.Folder, m.Repo)), opts, ui.DefaultFilterRowRenderer)
			if perr != nil {
				return perr
			}
			ok, cerr := confirmCrossEnvMapping(wsEnv, chosen, alias)
			if cerr != nil {
				return cerr
			}
			if !ok {
				continue
			}
			customer.Environments[envIdx].Deployments[mIdx].Workspace = chosen
			if err := config.EditCustomer(configPath, customerName, customer); err != nil {
				return fmt.Errorf("save customer: %w", err)
			}
			fmt.Printf("Mapping now %s → %s in env %q\n", mappingLabel(m.Folder, m.Repo), chosen, alias)
			return nil
		}
	case "baseline":
		baselineWS, perr := pickMappingBaseline(client, customerName, customer)
		if perr != nil {
			return perr
		}
		customer.Environments[envIdx].Deployments[mIdx].BaselineWorkspace = baselineWS
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			return fmt.Errorf("save customer: %w", err)
		}
		if baselineWS == "" {
			fmt.Printf("Mapping %s inherits the baseline environment.\n", mappingLabel(m.Folder, m.Repo))
		} else {
			fmt.Printf("Mapping %s resolves against dedicated baseline %q.\n", mappingLabel(m.Folder, m.Repo), baselineWS)
		}
	case "remove":
		deps := customer.Environments[envIdx].Deployments
		customer.Environments[envIdx].Deployments = append(deps[:mIdx:mIdx], deps[mIdx+1:]...)
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			return fmt.Errorf("save customer: %w", err)
		}
		fmt.Printf("Removed mapping %s → %s from env %q\n", mappingLabel(m.Folder, m.Repo), m.Workspace, alias)
	}
	return nil
}

// toggleSchedules flips whether .schedules definition parts deploy with their
// items or are excluded so each environment keeps its own schedules.
func toggleSchedules(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	current := func(v bool) string {
		if v == customer.SkipSchedules {
			return " (current)"
		}
		return ""
	}
	chosen, err := ui.NumberMenu("Deploy schedules?", []ui.MenuOption{
		{Label: "Deploy schedules with items" + current(false), Value: "deploy",
			Description: "The source's .schedules parts overwrite the target's — schedules follow git."},
		{Label: "Keep schedules in the target" + current(true), Value: "keep",
			Description: "Exclude .schedules from compare and deploy — schedules are managed per environment and survive deploys."},
	})
	if err != nil {
		return err
	}
	customer.SkipSchedules = chosen == "keep"
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	if customer.SkipSchedules {
		fmt.Println(infoStyle.Render("Schedules are kept in the target — .schedules is excluded from compare and deploy."))
	} else {
		fmt.Println(infoStyle.Render("Schedules deploy with their items."))
	}
	return nil
}

// toggleBulkBackend flips whether deploys use the bulk-import (preview)
// backend, replacing the old per-deploy question.
func toggleBulkBackend(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	current := func(v bool) string {
		if v == customer.UseBulkDeploy {
			return " (current)"
		}
		return ""
	}
	chosen, err := ui.NumberMenu("Which deploy backend?", []ui.MenuOption{
		{Label: "Per-item (stable, default)" + current(false), Value: "peritem",
			Description: "One API call per item. Slower on big deploys but battle-tested."},
		{Label: "Bulk-import (PREVIEW)" + current(true), Value: "bulk",
			Description: "Many items per API call on a Fabric beta API — verify deployed items afterwards."},
	})
	if err != nil {
		return err
	}
	customer.UseBulkDeploy = chosen == "bulk"
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save customer: %w", err)
	}
	if customer.UseBulkDeploy {
		fmt.Println(warningStyle.Render("Deploys now use the bulk-import PREVIEW backend — verify the deployed items afterwards."))
	} else {
		fmt.Println(infoStyle.Render("Deploys use the stable per-item backend."))
	}
	return nil
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
		options[i] = ui.MenuOption{Label: fmt.Sprintf("%s → %s%s", mappingLabel(d.Folder, d.Repo), d.Workspace, baselineSuffix(d)), Value: fmt.Sprintf("%d", i)}
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

// customerRepos returns every distinct repo a customer references: the primary
// RepoPath plus every per-mapping Repo across all environments, first-seen order.
func customerRepos(c config.Customer) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(c.RepoPath)
	for _, e := range c.Environments {
		for _, m := range e.Deployments {
			add(m.Repo)
		}
	}
	return out
}

// excludeItemTypes lets the user pick which item types to skip when comparing.
// The picker is populated from the item types actually present across all of
// the customer's repos (a local scan). Selected = excluded; default nothing
// selected = compare everything. Stored as Customer.ExcludedItemTypes.
func excludeItemTypes(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	repos := customerRepos(customer)
	if len(repos) == 0 {
		fmt.Println(infoStyle.Render("Set a repo path first (this customer has none) — can't list item types."))
		return ui.ErrGoBack
	}
	types, err := deploy.RepoItemTypesMulti(repos)
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
	repos := customerRepos(customer)
	if len(repos) == 0 {
		fmt.Println(infoStyle.Render("Set a repo path first (this customer has none) — can't list notebooks."))
		return ui.ErrGoBack
	}
	names, err := deploy.RepoItemNamesMulti(repos, "Notebook", "DataPipeline")
	if err != nil {
		return fmt.Errorf("scan repo for notebooks/pipelines: %w", err)
	}
	// Union of repo notebooks/pipelines and already-registered names, so a
	// registered name missing from the current scan isn't silently dropped on save.
	options := mergeSorted(names, customer.PostDeployRuns)
	if len(options) == 0 {
		fmt.Println(infoStyle.Render("No notebooks or pipelines found under the repo path."))
		return ui.ErrGoBack
	}

	chosen, err := ui.MultiSelect("Select notebooks/pipelines to offer as post-deploy runs (only deployed ones are offered per run)", options, customer.PostDeployRuns)
	if err != nil {
		return err
	}
	customer.PostDeployRuns = mergePostDeploySelection(customer.PostDeployRuns, chosen)
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save post-deploy runs: %w", err)
	}
	fmt.Println(infoStyle.Render("Saved post-deploy runs. Run order = list order in the config file."))
	return nil
}

// mergePostDeploySelection folds a fresh picker selection back into the
// existing (user-controlled) run order. ui.MultiSelect returns checked items
// in OPTIONS order (alphabetical via mergeSorted), so a plain assignment
// would silently reset a hand-ordered list to that alphabetical order — and
// list order IS the run order. Names still selected keep their existing
// position; newly-added names are appended in the order chosen returns them.
// Empty selection returns nil.
func mergePostDeploySelection(existing, chosen []string) []string {
	if len(chosen) == 0 {
		return nil
	}
	chosenSet := make(map[string]bool, len(chosen))
	for _, n := range chosen {
		chosenSet[n] = true
	}
	var ordered []string
	seen := make(map[string]bool, len(chosen))
	for _, n := range existing {
		if chosenSet[n] && !seen[n] {
			ordered = append(ordered, n)
			seen[n] = true
		}
	}
	for _, n := range chosen {
		if !seen[n] {
			ordered = append(ordered, n)
			seen[n] = true
		}
	}
	return ordered
}

// setRepoPath lets the user set the customer's primary Fabric git repo during
// setup, so repo-dependent pickers (exclude types, post-deploy runs) work before
// the first deploy — instead of RepoPath only being captured lazily at deploy time.
func setRepoPath(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	startDir, _ := os.UserHomeDir()
	if customer.RepoPath != "" {
		startDir = customer.RepoPath
		fmt.Println(currentValueBox("Current primary repo", customer.RepoPath))
	} else {
		fmt.Println(warningStyle.Render("No primary repo set yet."))
	}
	picked, err := ui.PickDirectory("Fabric git repo", startDir)
	if err != nil {
		return err
	}
	src, err := deploy.NewSourceAt(picked, customer.DeployBranch)
	if err != nil {
		return fmt.Errorf("not a usable git repo: %w", err)
	}
	customer.RepoPath = src.Repo()
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save repo path: %w", err)
	}
	fmt.Println(infoStyle.Render("Saved repo path: " + src.Repo()))
	return nil
}

// setDeployBranch pins (or clears) which origin branch this customer's deploys
// read from. When the repo is known, the user picks from origin's actual
// branches (listed live, so a branch just committed from a Fabric workspace is
// there without a local fetch); free text is the fallback when there is no
// repo yet or the branch listing fails. Empty/AUTO restores auto-detection of
// the remote's default.
func setDeployBranch(configPath, customerName string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customer, ok := cfg.Customers[customerName]
	if !ok {
		return fmt.Errorf("customer %q disappeared from config", customerName)
	}
	branch, picked, err := pickDeployBranch(customer)
	if err != nil {
		return err
	}
	if !picked {
		branch = customer.DeployBranch
		if err := runFormStep(huh.NewInput().
			Title("Deploy branch (empty = auto-detect the remote's default)").
			Value(&branch)); err != nil {
			return err
		}
		// Users naturally type "origin/dev" — the ref prefix is implied, strip it.
		branch = strings.TrimPrefix(strings.TrimSpace(branch), "origin/")
		if strings.ContainsAny(branch, " \t") {
			fmt.Println(warningStyle.Render("Branch names can't contain whitespace — nothing saved."))
			return nil
		}
	}
	customer.DeployBranch = branch
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save deploy branch: %w", err)
	}
	if customer.DeployBranch == "" {
		fmt.Println(infoStyle.Render("Deploy branch set to auto-detect (origin's default branch)."))
	} else {
		fmt.Println(infoStyle.Render("Deploys now read from origin/" + customer.DeployBranch + "."))
	}
	return nil
}

// pickDeployBranch offers origin's live branch list as a filter menu, plus an
// AUTO entry that clears the pin. picked=false (with no error) means the list
// couldn't be built — no repo configured yet, or git/origin unavailable — and
// the caller should fall back to free-text input. An error is the user backing
// out of the menu (ui.ErrGoBack) and must propagate, not fall through.
func pickDeployBranch(customer config.Customer) (branch string, picked bool, err error) {
	if customer.RepoPath == "" {
		return "", false, nil
	}
	sp := ui.NewSpinner("Listing branches on origin…")
	sp.Start()
	branches, lerr := deploy.ListRemoteBranches(customer.RepoPath)
	sp.Stop()
	if lerr != nil || len(branches) == 0 {
		return "", false, nil
	}
	// \x00 can't collide with a real branch name (git forbids NUL in refs).
	const autoValue = "\x00auto"
	autoLabel := "AUTO — detect the remote's default branch"
	if customer.DeployBranch == "" {
		autoLabel += "  (current)"
	}
	opts := []ui.FilterOption{{Label: autoLabel, Value: autoValue}}
	for _, b := range branches {
		label := b
		if b == customer.DeployBranch {
			label += "  (current pin)"
		}
		opts = append(opts, ui.FilterOption{Label: label, Value: b})
	}
	choice, err := ui.FilterMenu("Deploy branch", opts, ui.DefaultFilterRowRenderer)
	if err != nil {
		return "", false, err
	}
	if choice == autoValue {
		return "", true, nil
	}
	return choice, true, nil
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
