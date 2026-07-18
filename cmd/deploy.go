package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// excludedSet is the customer's per-type exclusion as a lookup. Empty = nothing
// excluded (compare every discovered type).
func excludedSet(customer config.Customer) map[string]bool {
	s := make(map[string]bool, len(customer.ExcludedItemTypes))
	for _, t := range customer.ExcludedItemTypes {
		s[t] = true
	}
	return s
}

// filterExcludedTypes drops local items whose type the customer excluded.
func filterExcludedTypes(items []deploy.LocalItem, excluded map[string]bool) []deploy.LocalItem {
	out := make([]deploy.LocalItem, 0, len(items))
	for _, it := range items {
		if !excluded[it.Type] {
			out = append(out, it)
		}
	}
	return out
}

// localTypeScope is the set of item types present in the local items — used as
// the orphan scope so only types you actually manage in the repo can be flagged
// Orphan (target-only system/auto items are never flagged). Orphans are shown,
// never deleted (Execute has create/update only).
func localTypeScope(items []deploy.LocalItem) map[string]bool {
	s := map[string]bool{}
	for _, it := range items {
		s[it.Type] = true
	}
	return s
}

// repoInputsForAlias returns the distinct repo config-strings to discover for an
// environment: the customer's primary RepoPath (when set) first, then every
// distinct non-empty per-mapping Repo, in first-seen order. Empty strings are
// dropped (a customer with no primary and mappings without a Repo yields nothing
// — the caller still falls back to the interactive repo picker).
func repoInputsForAlias(customer config.Customer, alias string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(customer.RepoPath)
	mappings, _ := customer.DeployMappings(alias)
	for _, m := range mappings {
		add(m.Repo)
	}
	return out
}

// deployGroup is one folder→workspace mapping resolved for a run: the items
// discovered under that folder, the target workspace, the compare rows, and the
// deployed item list (needed by BuildPlan).
type deployGroup struct {
	Folder         string
	Target         fabric.Workspace
	Rows           []deploy.CompareRow
	Deployed       []fabric.Item
	Unresolved     []deploy.UnresolvedRef
	Changes        []deploy.RebindChange
	ReportBindings []deploy.ReportBinding
	Diffs          []ItemDiff
	// rb is the rebinder this group's mapping resolves references through — the
	// shared env-level one, or the mapping's isolated one when it sets a
	// BaselineWorkspace. Nil when rebinding is disabled.
	rb *deploy.Rebinder
}

// ItemDiff holds the per-part content diffs for one Changed item, for the HTML
// diff viewer.
type ItemDiff struct {
	Name  string
	Type  string
	Parts []deploy.PartDiff
}

// Deploy is the top-level entry point for the `deploy` subcommand.
func Deploy(configPath string) error { return DeployWithAPI(configPath, DefaultAPI) }

// DeployWithAPI: pick customer → resolve source from origin/main → pick
// environment → resolve its folder→workspace mappings → compare per group →
// dry-run or cherry-pick+publish.
func DeployWithAPI(configPath string, client APIClient) error {
	// Wrap the client in a per-run memo so every ListItems(workspaceID) call
	// (buildDeployGroups, BuildNameIndex, Resolver.findItem, pickTargetItem)
	// hits the Fabric API at most once per workspace. All callers are in the
	// compare/index phase — before Execute or DeleteItems mutate anything —
	// so the cached list is never stale.
	client = newMemoClient(client)

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
		return fmt.Errorf("customer %q has no environments — add one via Manage customers → Edit customer first", customerName)
	}

	alias, err := pickEnvironment(customer)
	if err != nil {
		return err
	}

	// Discover items per repo the environment references. Empty repo list means a
	// brand-new customer with no RepoPath and no per-mapping repos yet — fall back
	// to the interactive picker and treat the chosen dir as the primary repo.
	repoInputs := repoInputsForAlias(customer, alias)
	if len(repoInputs) == 0 {
		// First-run setup, question 1: where does the code live? One headline,
		// one instruction — the picker itself just shows the breadcrumb.
		fmt.Println()
		fmt.Println(infoStyle.Render("Primary repo path — where does the code live?"))
		fmt.Println(wrapIndented("Navigate to the repo root and pick \"✓ Use this folder\". futils deploys from origin/<default-branch>, never the working tree.", 2))
		startDir, _ := os.UserHomeDir()
		picked, perr := ui.PickDirectory("Fabric git repo", startDir)
		if perr != nil {
			return perr
		}
		// The picked repo becomes the primary one, so the branch pin applies.
		src, serr := deploy.NewSourceAt(picked, customer.DeployBranch)
		if serr != nil {
			return serr
		}
		customer.RepoPath = src.Repo()
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			fmt.Println(warningStyle.Render("Couldn't save repo path: " + err.Error()))
		} else {
			fmt.Println(infoStyle.Render(fmt.Sprintf("Saved repo path for %s: %s", customerName, src.Repo())))
		}
		repoInputs = []string{customer.RepoPath}
	}

	// First-run setup, question 2: which environment does the git code belong
	// to? Asked back-to-back with the repo question — both are about anchoring
	// the source side — and BEFORE the fetch, so all setup questions come
	// before the waiting starts. An explicit Skip keeps the old no-rebind
	// behavior for this run.
	if customer.BaselineEnvironment == "" && len(customer.Environments) > 0 {
		chosen, perr := promptFirstRunBaseline(customer)
		if perr != nil {
			return perr
		}
		if chosen != "" {
			customer.BaselineEnvironment = chosen
			if serr := config.EditCustomer(configPath, customerName, customer); serr != nil {
				fmt.Println(warningStyle.Render("Couldn't save baseline environment: " + serr.Error()))
			} else {
				fmt.Println(infoStyle.Render(fmt.Sprintf("Baseline environment set to %q — saved to %s.", chosen, customerName)))
			}
		}
	}

	itemsByRepo := make(map[string][]deploy.LocalItem, len(repoInputs))
	totalItems := 0
	for _, input := range repoInputs {
		src, serr := deploy.NewSourceAt(input, customer.BranchForRepo(input))
		if serr != nil {
			return fmt.Errorf("repo %q: %w", input, serr)
		}
		sp := ui.NewSpinner(fmt.Sprintf("Fetching and reading %s (%s)...", input, src.Ref()))
		sp.Start()
		var items []deploy.LocalItem
		var fetchErr, discErr error
		func() {
			defer sp.Stop()
			if fetchErr = src.Fetch(); fetchErr != nil {
				return
			}
			items, discErr = src.DiscoverItems()
		}()
		if fetchErr != nil {
			return fmt.Errorf("repo %q: %w", input, fetchErr)
		}
		if discErr != nil {
			return fmt.Errorf("repo %q: %w", input, discErr)
		}
		itemsByRepo[input] = items
		totalItems += len(items)
	}
	if totalItems == 0 {
		return fmt.Errorf("no Fabric items found across %d repo(s)", len(repoInputs))
	}

	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	rebinders, err := newRebinderSet(client, token, customer, alias, workspaces)
	if err != nil {
		return fmt.Errorf("set up reference rebinding: %w", err)
	}
	if customer.BaselineEnvironment == "" {
		fmt.Println(infoStyle.Render("Auto-rebind disabled (no baseline environment set). Set one via Manage customers → Edit customer to translate references by name."))
	}

	mappings, _ := customer.DeployMappings(alias)
	if len(mappings) == 0 {
		// Auto-setup only runs before any mappings exist, so it always offers
		// the primary repo's items (customers with extra per-mapping repos add
		// those manually afterward).
		mappings, err = setupDeployMappings(itemsByRepo[customer.RepoPath], workspaces, customer, alias)
		if err != nil {
			return err
		}
		if len(mappings) == 0 {
			return fmt.Errorf("no folder mappings configured for %q — nothing to deploy", alias)
		}
		if idx := findEnvIndex(customer, alias); idx >= 0 {
			customer.Environments[idx].Deployments = mappings
			if err := config.EditCustomer(configPath, customerName, customer); err != nil {
				return fmt.Errorf("save deployment mappings: %w", err)
			}
			fmt.Println(infoStyle.Render(fmt.Sprintf("Saved %d mapping(s) to env %q.", len(mappings), alias)))
		}
	}

	mappings, err = pickDeployScope(mappings)
	if err != nil {
		return err
	}

	env := alias

	excluded := excludedSet(customer)
	if len(customer.ExcludedItemTypes) > 0 {
		// Sort a copy: customer is a struct copy but its slice shares the backing
		// array with the in-memory config, so sorting in place would reorder it.
		shown := append([]string(nil), customer.ExcludedItemTypes...)
		sort.Strings(shown)
		fmt.Println(infoStyle.Render("Excluded item types: " + strings.Join(shown, ", ")))
	}

	groups, err := buildDeployGroups(client, token, customer, mappings, itemsByRepo, workspaces, rebinders, excluded)
	if err != nil {
		return err
	}

	filterIgnoredUnresolved(groups, customer)

	// Always compare first, then gate on an explicit "continue to deployment".
	fmt.Println(infoStyle.Render(fmt.Sprintf("Comparing %s → %s", env, targetsSummary(groups))))
	printGroupedCompare(groups)
	printRebindSummary(groups)
	printReportBindings(groups)
	printUnresolved(groups, customer.BaselineEnvironment, alias)

	hasDiffs := false
	for _, g := range groups {
		if len(g.Diffs) > 0 {
			hasDiffs = true
			break
		}
	}
	if hasDiffs {
		if ok, cerr := ui.Confirm("Open content diffs in browser?"); cerr == nil && ok {
			if derr := showDiffsInBrowser(groups); derr != nil {
				fmt.Println(warningStyle.Render("Couldn't open diffs: " + derr.Error()))
			}
		}
	}

	var unresolved []deploy.UnresolvedRef
	for _, g := range groups {
		unresolved = append(unresolved, g.Unresolved...)
	}
	if len(unresolved) > 0 {
		if ok, cerr := ui.Confirm(fmt.Sprintf("Map %d unresolved reference(s) now?", len(unresolved))); cerr == nil && ok {
			if merr := mapUnresolvedInteractive(client, token, configPath, customerName, customer, unresolved); merr != nil {
				fmt.Println(warningStyle.Render("Mapping aborted: " + merr.Error()))
			} else {
				// Mapping changed config; the user re-runs deploy to pick it up.
				fmt.Println(infoStyle.Render("References updated — re-run deploy to apply them."))
			}
			return nil
		}
	}

	cont, err := ui.Confirm("Continue to deployment?")
	if err != nil {
		return err
	}
	if !cont {
		return nil // declined: this was a dry-run
	}

	// Backend choice is a per-customer setting (Edit customer → Bulk-import
	// backend) instead of a per-deploy question.
	useBulk := customer.UseBulkDeploy
	if useBulk {
		fmt.Println(warningStyle.Render("Bulk-import is a PREVIEW backend on a Fabric beta API — verify the deployed items afterwards."))
	}

	results, err := runDeploy(client, token, groups, pickGroupedRows, ui.Confirm, useBulk)
	printDeployResults(results)
	if err != nil {
		return err
	}
	// Before the post-deploy notebooks: they may consume the variables, so the
	// right value set must be active when they run.
	activateVariableLibraries(client, token, alias, groups, results)
	// Also before the post-deploy notebooks: a notebook attached to a deployed
	// environment should run against its published settings, not stale ones.
	publishEnvironments(client, token, results, ui.Confirm)
	runOutcomes := offerPostDeployRuns(client, token, customer, groups, results)
	saveDeployHistory(configPath, customerName, customer, groups, results, runOutcomes, ui.Confirm)
	return nil
}

// diffConcurrency bounds how many deployed item definitions are fetched in
// parallel during the compare. The reference dry-run runs 20 wide with a 1s
// poll and never throttles, which showed concurrency was never the limiter —
// the request *rate* (poll cadence) was. With the 1s poll floor in place, 16 is
// safe and well above the slow 4; Fabric 429s (if any) still self-limit via the
// client's Retry-After backoff and are surfaced in the spinner.
const diffConcurrency = 16

// buildDeployGroups turns each folder→workspace mapping into a compare group:
// items under that folder vs the mapped workspace's deployed items. For items
// that already exist it runs a content-diff (concurrent definition fetches +
// per-part normalized comparison) to refine ClassExists into ClassChanged or
// ClassUnchanged; items it can't verify stay ClassExists.
func buildDeployGroups(client APIClient, token string, customer config.Customer, mappings []config.DeployMapping, itemsByRepo map[string][]deploy.LocalItem, workspaces []fabric.Workspace, rs *rebinderSet, excluded map[string]bool) ([]deployGroup, error) {
	groups := make([]deployGroup, 0, len(mappings))
	for _, m := range mappings {
		target, err := resolveWorkspaceByName(workspaces, m.Workspace)
		if err != nil {
			return nil, fmt.Errorf("mapping %q→%q: %w", m.Folder, m.Workspace, err)
		}
		rb, err := rs.For(m)
		if err != nil {
			return nil, fmt.Errorf("mapping %q→%q: %w", m.Folder, m.Workspace, err)
		}

		repoKey := customer.MappingRepo(m)
		repoItems, ok := itemsByRepo[repoKey]
		if !ok {
			// The mapping points at a repo the discovery step never scanned (e.g. a
			// hand-edited config, or a customer with no primary repo and a mapping
			// that omits Repo). Warn instead of silently deploying an empty set.
			fmt.Println(warningStyle.Render(fmt.Sprintf("Mapping %q→%q references repo %q, which wasn't discovered — deploying nothing for it. Check the customer's repo config with `futils edit`.", m.Folder, m.Workspace, repoKey)))
		}
		items := filterExcludedTypes(deploy.ItemsInFolder(repoItems, m.Folder), excluded)
		if customer.SkipSchedules {
			// Both compare and publish flow from these items, so stripping here
			// keeps the diff, the plan, and the bulk payload consistent.
			items = deploy.StripScheduleParts(items)
		}
		deployed, err := client.ListItems(token, target.ID)
		if err != nil {
			return nil, fmt.Errorf("list items in %s: %w", target.DisplayName, err)
		}
		rows := deploy.Compare(items, deployed, localTypeScope(items))
		g := deployGroup{
			Folder:   m.Folder,
			Target:   target,
			Rows:     rows,
			Deployed: deployed,
			rb:       rb,
		}
		g.Unresolved, g.Changes, g.Diffs = diffExistingRows(client, token, target, rows, rb, customer.SkipSchedules)
		if rb != nil {
			// Existing (content-compared) reports get their custom-substitution
			// unresolved via diffExistingRows; NEW reports only pass through here,
			// so this pass also owns their substitution refs.
			newReport := map[string]bool{}
			for _, r := range g.Rows {
				if r.ItemType() == "Report" && r.Class == deploy.ClassNew {
					newReport[r.Name()] = true
				}
			}
			for _, it := range items {
				if it.Type != "Report" {
					continue
				}
				for _, part := range it.Parts {
					if path.Base(part.Path) != "definition.pbir" {
						continue
					}
					// Match the publish pipeline: apply the customer's find→replace
					// substitutions to the pbir before resolving the binding, so the
					// previewed binding can't differ from what SubstituteParts publishes.
					subbed, subOutcome := rb.ApplyCustomSubstitutions(it, part.Path, part.Content)
					_, outcome := rb.RebindReportConnection(it, subbed)
					g.ReportBindings = append(g.ReportBindings, outcome.ReportBindings...)
					unres := outcome.Unresolved
					if newReport[it.DisplayName] {
						unres = append(unres, subOutcome.Unresolved...)
					}
					for i := range unres {
						unres[i].ItemName = it.DisplayName
					}
					g.Unresolved = append(g.Unresolved, unres...)
					break // definition.pbir is the only part that carries the binding
				}
			}
		}
		groups = append(groups, g)
	}
	reconcileOrphans(groups)
	return groups, nil
}

func rowKey(r deploy.CompareRow) string { return r.ItemType() + "\x00" + r.Name() }

// reconcileOrphans corrects orphan classification across deploy groups that
// share a target workspace. Each group's Compare runs that folder's local items
// against the workspace's FULL deployed list, so a sibling folder's valid items
// get mislabeled ClassOrphan, and a genuine orphan surfaces in every sibling
// group. With orphans now deletable, that would offer valid items for deletion
// and duplicate true orphans. This keeps an Orphan row only when the item is
// absent from EVERY folder mapping to that workspace, and keeps each genuine
// orphan exactly once. It's a no-op when each workspace has a single mapping.
func reconcileOrphans(groups []deployGroup) {
	wsLocal := map[string]map[string]bool{} // workspace ID -> set of locally-managed type+name keys
	for _, g := range groups {
		keys := wsLocal[g.Target.ID]
		if keys == nil {
			keys = map[string]bool{}
			wsLocal[g.Target.ID] = keys
		}
		for _, r := range g.Rows {
			if r.Class != deploy.ClassOrphan {
				keys[rowKey(r)] = true
			}
		}
	}
	kept := map[string]bool{} // workspace+key already kept as an orphan once
	for gi := range groups {
		ws := groups[gi].Target.ID
		out := groups[gi].Rows[:0]
		for _, r := range groups[gi].Rows {
			if r.Class == deploy.ClassOrphan {
				k := rowKey(r)
				if wsLocal[ws][k] {
					continue // a sibling folder deploys this — not a real orphan
				}
				dk := ws + "\x00" + k
				if kept[dk] {
					continue // a sibling group already surfaced this orphan
				}
				kept[dk] = true
			}
			out = append(out, r)
		}
		groups[gi].Rows = out
	}
}

// filterIgnoredUnresolved drops any unresolved reference the customer marked
// ignore, so it isn't re-surfaced on every deploy. Mutates groups in place.
func filterIgnoredUnresolved(groups []deployGroup, customer config.Customer) {
	for gi := range groups {
		kept := groups[gi].Unresolved[:0]
		for _, u := range groups[gi].Unresolved {
			if !customer.IsIgnored(u.GUID) {
				kept = append(kept, u)
			}
		}
		groups[gi].Unresolved = kept
	}
}

// newItemSentinel returns a deterministic placeholder for a ClassNew item's
// logicalId during compare. It can never equal a real Fabric GUID or any
// deployed content, so DiffParts will always report the referencing row as
// Changed — accurately predicting that publish will substitute a fresh GUID.
// The value is human-readable in HTML diff output.
func newItemSentinel(logicalID string) string {
	return "futils:pending-new-item:" + logicalID
}

// diffExistingRows fetches the deployed definition of every ClassExists row
// (concurrently, bounded) and reclassifies it ClassChanged or ClassUnchanged by
// comparing against the local item's substituted parts. Rows whose definition
// can't be fetched or substituted stay ClassExists (unverified) and a warning
// is printed with the count and first reason. Mutates rows in place.
func diffExistingRows(client deploy.FabricClient, token string, target fabric.Workspace, rows []deploy.CompareRow, rb *deploy.Rebinder, skipSchedules bool) ([]deploy.UnresolvedRef, []deploy.RebindChange, []ItemDiff) {
	// Zero-part items (Warehouse, SQLDatabase — shell types with nothing but a
	// .platform in git) have no definition to fetch or diff: getDefinition 400s
	// with OperationNotSupportedForItem for them, which used to leave every such
	// row as a noisy unverified Exists. The only deployable field is the
	// description (synced via UpdateItem), so classify on that alone.
	var shellDiffs []ItemDiff
	var existsIdx []int
	for i := range rows {
		if rows[i].Class != deploy.ClassExists {
			continue
		}
		if len(rows[i].Local.Parts) == 0 {
			if rows[i].Local.Description == rows[i].Deployed.Description {
				rows[i].Class = deploy.ClassUnchanged
			} else {
				rows[i].Class = deploy.ClassChanged
				shellDiffs = append(shellDiffs, ItemDiff{
					Name: rows[i].Name(),
					Type: rows[i].ItemType(),
					Parts: []deploy.PartDiff{{
						Path: "(item description)",
						Old:  rows[i].Deployed.Description,
						New:  rows[i].Local.Description,
					}},
				})
			}
			continue
		}
		existsIdx = append(existsIdx, i)
	}
	if len(existsIdx) == 0 {
		return nil, nil, shellDiffs
	}

	total := len(existsIdx)
	var done int64
	baseThrottle := fabric.ThrottleHits()
	fabric.ResetThrottleFirst() // scope the "first 429" detail to this group's compare
	render := func() string {
		msg := fmt.Sprintf("Comparing %d/%d item(s) in %s", atomic.LoadInt64(&done), total, target.DisplayName)
		if status := liveThrottleStatus(); status != "" {
			return msg + status
		}
		return msg + "..."
	}
	// The spinner repaints render() on every frame, so the throttle suffix (green
	// countdown bar + retry + waiting count) stays fresh even during a 429 stall —
	// when every worker is sleeping and the done counter is frozen, the repaint
	// still animates the bar toward the retry deadline.
	sp := ui.NewSpinner(render())
	sp.SetMessageFunc(render)
	sp.Start()
	type fetched struct {
		def *fabric.Definition
		err error
	}
	results := make([]fetched, len(existsIdx))
	sem := make(chan struct{}, diffConcurrency)
	var wg sync.WaitGroup
	for j, idx := range existsIdx {
		wg.Add(1)
		sem <- struct{}{}
		go func(j, idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			def, err := client.GetItemDefinition(token, target.ID, rows[idx].DeployedID, "")
			if skipSchedules && def != nil {
				// Local parts were stripped before Compare; strip the deployed
				// side too, or every scheduled target item would diff as a
				// phantom removed-.schedules part.
				kept := def.Parts[:0:0]
				for _, p := range def.Parts {
					if path.Base(p.Path) != ".schedules" {
						kept = append(kept, p)
					}
				}
				def.Parts = kept
			}
			results[j] = fetched{def: def, err: err}
			atomic.AddInt64(&done, 1)
		}(j, idx)
	}
	wg.Wait()
	// sp.Stop() is NOT called here — the spinner stays live through the compare
	// loop below so any 429 that hits SubstituteParts (which makes live API calls)
	// still shows the throttle countdown instead of looking like a silent hang.

	// Map each source logicalId to its deployed GUID so cross-item references in
	// the local definition match what's live in the workspace.
	//
	// ClassNew items are not yet in the workspace; publish will create them and
	// assign a fresh GUID, so any Exists item that references a ClassNew dep's
	// logicalId WILL change on publish. We substitute a sentinel so DiffParts
	// reports the ref as Changed instead of falsely Unchanged.
	compareIDs := map[string]string{}
	for _, r := range rows {
		if r.Class == deploy.ClassExists && r.Local.LogicalID != "" {
			compareIDs[r.Local.LogicalID] = r.DeployedID
		} else if r.Class == deploy.ClassNew && r.Local.LogicalID != "" {
			compareIDs[r.Local.LogicalID] = newItemSentinel(r.Local.LogicalID)
		}
	}
	resolver := deploy.NewResolver(client, token, target)

	// The per-item compare work (logicalId+param substitution, name-based
	// rebind, JSON normalize/diff) is CPU-bound and runs once per changed item.
	// Done serially it freezes the spinner on a big workspace, so fold it into
	// the same bounded pool the fetch used. The shared resolver and rb have
	// lazy caches (guarded by their own mutexes), so concurrent use is safe.
	//
	// SubstituteParts makes live API calls (e.g. name-based rebind lookups), so
	// the spinner remains active through this loop: a 429 here shows the throttle
	// countdown rather than looking like a hang.
	//
	// Each worker writes ONLY its own pre-sized slot — no shared appends, no
	// shared map/field writes — then a serial merge pass folds the slots in
	// deterministic existsIdx order. That keeps the output (reclassifications,
	// itemDiffs/changes/unresolved sets AND their order) byte-for-byte identical
	// to the old serial loop; only the wall-clock changes.
	type compareResult struct {
		class      deploy.Class
		itemDiff   *ItemDiff
		unresolved []deploy.UnresolvedRef
		changes    []deploy.RebindChange
		err        error
	}
	compared := make([]compareResult, len(existsIdx))
	csem := make(chan struct{}, diffConcurrency)
	var cwg sync.WaitGroup
	for j, idx := range existsIdx {
		if results[j].err != nil {
			compared[j] = compareResult{err: results[j].err}
			continue
		}
		cwg.Add(1)
		csem <- struct{}{}
		go func(j, idx int) {
			defer cwg.Done()
			defer func() { <-csem }()
			localParts, outcome, perr := deploy.SubstituteParts(rows[idx].Local, compareIDs, resolver, rb)
			res := compareResult{
				unresolved: outcome.Unresolved,
				changes:    outcome.Changes,
			}
			if perr != nil {
				res.err = perr
				compared[j] = res
				return
			}
			// Description lives in .platform (excluded from the part diff) and is
			// deployed separately via UpdateItem, so drift in it is a real change.
			// DiffParts is the single source of the content verdict (non-empty ==
			// changed), avoiding a redundant PartsChanged normalization pass.
			deployedDesc := deploy.DeployedDescription(results[j].def)
			descChanged := deployedDesc != rows[idx].Local.Description
			parts := deploy.DiffParts(localParts, results[j].def)
			if len(parts) > 0 || descChanged {
				res.class = deploy.ClassChanged
				if descChanged {
					parts = append(parts, deploy.PartDiff{
						Path: "(item description)", Old: deployedDesc, New: rows[idx].Local.Description,
					})
				}
				res.itemDiff = &ItemDiff{
					Name:  rows[idx].Name(),
					Type:  rows[idx].ItemType(),
					Parts: parts,
				}
			} else {
				res.class = deploy.ClassUnchanged
			}
			compared[j] = res
		}(j, idx)
	}
	cwg.Wait()
	sp.Stop()

	// One-line, after-the-fact note when Fabric rate-limited us during the
	// compare — explains why it was slow without spamming a line per 429.
	// Printed after sp.Stop() so it doesn't write over a live spinner frame.
	if throttled := fabric.ThrottleHits() - baseThrottle; throttled > 0 {
		msg := fmt.Sprintf("Fabric rate-limited the compare %d time(s) — that's the slowness, not a hang.", throttled)
		if info := fabric.FirstThrottle(); info != "" {
			msg += "\n  first 429: " + info
		}
		fmt.Println(warningStyle.Render(msg))
	}

	// Serial merge in existsIdx order: preserves the exact accumulation order
	// the old loop produced. SubstituteParts always returns its outcome
	// (changes/unresolved) even on error, so those are merged regardless — the
	// old loop appended them before the perr check too.
	var unverified int
	var firstErr error
	var unresolved []deploy.UnresolvedRef
	var changes []deploy.RebindChange
	itemDiffs := shellDiffs
	for j, idx := range existsIdx {
		c := compared[j]
		if results[j].err != nil {
			unverified++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rows[idx].Name(), results[j].err)
			}
			continue
		}
		// Report BINDING refs (Location == LocationReportBinding) are owned by the
		// dedicated pass in buildDeployGroups (it runs for every report, new or
		// existing) — dropping them here avoids a double report. Everything else a
		// report part produced (e.g. custom-substitution refs) is collected exactly
		// like for any other item type; the dedicated pass does NOT re-collect
		// those for existing reports. Substitution Changes are collected for every
		// item — for a report these are the only Changes (the binding rewrite
		// emits none), so there is no double-count.
		if rows[idx].ItemType() == "Report" {
			for _, u := range c.unresolved {
				if u.Location != deploy.LocationReportBinding {
					unresolved = append(unresolved, u)
				}
			}
		} else {
			unresolved = append(unresolved, c.unresolved...)
		}
		changes = append(changes, c.changes...)
		if c.err != nil {
			unverified++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rows[idx].Name(), c.err)
			}
			continue
		}
		rows[idx].Class = c.class
		if c.itemDiff != nil {
			itemDiffs = append(itemDiffs, *c.itemDiff)
		}
	}
	if unverified > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf(
			"%d of %d item(s) in %s couldn't be content-compared (shown as Exists). First reason: %v",
			unverified, len(existsIdx), target.DisplayName, firstErr)))
	}
	return unresolved, changes, itemDiffs
}

// setupDeployMappings asks the user which workspace each repo folder deploys to,
// using the folders discovered in the repo as the pick-list. Folders the user
// skips are left unmapped. Returns the chosen mappings (possibly empty).
func setupDeployMappings(all []deploy.LocalItem, workspaces []fabric.Workspace, customer config.Customer, alias string) ([]config.DeployMapping, error) {
	folders := deploy.TopLevelFolders(all)
	if len(folders) == 0 {
		return nil, fmt.Errorf("couldn't detect any folders to map in the repo — add mappings via Manage customers → Edit customer instead")
	}
	const skipValue = "\x00skip"
	fmt.Println()
	fmt.Println(infoStyle.Render(fmt.Sprintf("Deployment mappings — which repo folder deploys to which %s workspace?", alias)))
	fmt.Println(wrapIndented(fmt.Sprintf("Each mapping is saved on env %s: every %s deploy sends that folder's items to its workspace. Skip folders that shouldn't deploy.", alias, alias), 2))

	baseOpts, wsEnv := mappingWorkspaceOptions(workspaces, customer, alias)

	var mappings []config.DeployMapping
	for _, folder := range folders {
		opts := append([]ui.FilterOption{{Label: "⋯ Skip this folder", Value: skipValue}}, baseOpts...)
		for {
			chosen, err := ui.FilterMenu(fmt.Sprintf("Map %s/ → which workspace?", folder), opts, ui.DefaultFilterRowRenderer)
			if err != nil {
				return nil, err
			}
			if chosen == skipValue {
				break
			}
			if ok, cerr := confirmCrossEnvMapping(wsEnv, chosen, alias); cerr != nil {
				return nil, cerr
			} else if !ok {
				continue // re-show the picker for this folder
			}
			mappings = append(mappings, config.DeployMapping{Folder: folder, Workspace: chosen})
			fmt.Printf("  %s/ → %s\n", folder, chosen)
			break
		}
	}
	return mappings, nil
}

// mappingWorkspaceOptions orders workspaces for a mapping picker: the target
// env's own workspaces (sorted) lead, everything else follows — the right
// answer is almost always one of the env's own. Also returns the
// workspace→env map used to challenge cross-env choices.
func mappingWorkspaceOptions(workspaces []fabric.Workspace, customer config.Customer, alias string) ([]ui.FilterOption, map[string]string) {
	wsEnv := map[string]string{}
	for _, e := range customer.Environments {
		for _, w := range e.Workspaces {
			wsEnv[w] = e.Alias
		}
	}
	var envWS, otherWS []string
	for _, w := range workspaces {
		if wsEnv[w.DisplayName] == alias {
			envWS = append(envWS, w.DisplayName)
		} else {
			otherWS = append(otherWS, w.DisplayName)
		}
	}
	sort.Strings(envWS)
	sort.Strings(otherWS)
	opts := make([]ui.FilterOption, 0, len(envWS)+len(otherWS))
	for _, w := range envWS {
		opts = append(opts, ui.FilterOption{Label: w, Value: w})
	}
	for _, w := range otherWS {
		opts = append(opts, ui.FilterOption{Label: w, Value: w})
	}
	return opts, wsEnv
}

// confirmCrossEnvMapping challenges a workspace choice registered on a
// DIFFERENT environment — mapping TEST's folder to a DEV workspace is almost
// always a slip that would deploy TEST-rebound content into DEV. Returns true
// when the choice stands (same env, unregistered workspace, or confirmed).
func confirmCrossEnvMapping(wsEnv map[string]string, chosen, alias string) (bool, error) {
	owner, ok := wsEnv[chosen]
	if !ok || owner == alias {
		return true, nil
	}
	return ui.Confirm(fmt.Sprintf("%s is registered on env %s, not %s — map %s's folder there anyway?", chosen, owner, alias, alias))
}

// deleteCount returns the total number of items to be deleted across all groups.
func deleteCount(m map[int][]deploy.DeleteTarget) int {
	n := 0
	for _, d := range m {
		n += len(d)
	}
	return n
}

// printSelectedItems lists exactly what the deploy confirm is about to
// publish, grouped per target workspace — the picker's collapsed summary only
// says "N selected", which is not enough to sanity-check before committing.
// Long selections are capped so a full-repo deploy doesn't scroll the compare
// table away.
func printSelectedItems(groups []deployGroup, selected map[int][]deploy.LocalItem) {
	const maxLines = 20
	shown := 0
	remaining := 0
	for gi, g := range groups {
		items := selected[gi]
		if len(items) == 0 {
			continue
		}
		fmt.Println(infoStyle.Render(fmt.Sprintf("→ %s:", g.Target.DisplayName)))
		for _, it := range items {
			if shown >= maxLines {
				remaining++
				continue
			}
			fmt.Printf("    %-18s %s\n", it.Type, it.DisplayName)
			shown++
		}
	}
	if remaining > 0 {
		fmt.Printf("    … and %d more\n", remaining)
	}
}

// runDeploy lets the user cherry-pick across groups, confirms, and executes each
// group against its own workspace. Returns the aggregated per-item results. On a
// mid-run Execute failure it returns the results accumulated so far alongside the
// error, so callers should print results before checking err.
func runDeploy(
	client deploy.FabricClient,
	token string,
	groups []deployGroup,
	selectItems func([]deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error),
	confirm func(string) (bool, error),
	bulk bool,
) ([]deploy.Result, error) {
	selected, deletesByGroup, err := selectItems(groups)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, items := range selected {
		total += len(items)
	}
	nDel := deleteCount(deletesByGroup)
	if total == 0 && nDel == 0 {
		fmt.Println("Nothing selected.")
		return nil, nil
	}

	var allResults []deploy.Result
	// Deploy (create/update). Skipped entirely on a delete-only run so we never
	// prompt "Deploy 0 item(s)?" — the delete flow below handles orphan-only runs.
	if total > 0 {
		wsCount := 0
		for _, items := range selected {
			if len(items) > 0 {
				wsCount++
			}
		}
		modeLabel := "per-item"
		if bulk {
			modeLabel = "bulk-import (preview)"
		}
		printSelectedItems(groups, selected)
		ok, err := confirm(fmt.Sprintf("Deploy %d item(s) across %d workspace(s) using the %s backend?", total, wsCount, modeLabel))
		if err != nil {
			return nil, err
		}
		if !ok {
			// Cancelling the deploy aborts the whole run, including any selected
			// deletes — re-run and pick only orphans for a delete-only pass.
			fmt.Println("Cancelled.")
			return allResults, nil
		}

		if bulk {
			bulkResults, berr := runBulkPublish(client, token, groups, selected)
			allResults = append(allResults, bulkResults...)
			if berr != nil {
				if nDel > 0 {
					fmt.Println(warningStyle.Render(fmt.Sprintf("Deploy failed — the %d selected delete(s) were NOT run.", nDel)))
				}
				return allResults, berr
			}
		} else {
			// modelsByWS accumulates every published SemanticModel across ALL groups,
			// keyed by target workspace, so report rebinds (deferred to the pass below)
			// see models regardless of which group deployed them — and never bind a
			// model from a different workspace. pending collects the report rebinds to
			// run once everything is published.
			modelsByWS := map[string]map[string]string{}
			var pending []deploy.PendingReportRebind
			for i, g := range groups {
				items := selected[i]
				if len(items) == 0 {
					continue
				}
				plan := deploy.BuildPlan(items, g.Deployed)
				// done advances once per published item (Execute increments it); the
				// render func reads it concurrently every frame, so the spinner shows
				// "Publishing X/Y" live and — when a 429 stalls the workers — the green
				// countdown bar instead of a frozen line.
				var done int64
				total := len(plan)
				ws := g.Target.DisplayName
				renderPublish := func() string {
					return fmt.Sprintf("Publishing %d/%d to %s", atomic.LoadInt64(&done), total, ws) +
						liveThrottleStatus()
				}
				sp := ui.NewSpinner(renderPublish())
				sp.SetMessageFunc(renderPublish)
				sp.Start()
				results, groupPending, execErr := deploy.Execute(client, token, g.Target, plan, g.rb, modelsByWS, &done)
				sp.Stop()
				allResults = append(allResults, results...)
				pending = append(pending, groupPending...)
				if execErr != nil {
					if nDel > 0 {
						fmt.Println(warningStyle.Render(fmt.Sprintf("Deploy failed — the %d selected delete(s) were NOT run.", nDel)))
					}
					return allResults, execErr
				}
			}

			// Post-deploy rebind pass: now that every group is published, repoint each
			// report at its model and fold the outcome into the report's Result (matched
			// by deployed GUID). Runs BEFORE the delete pass.
			// Report rebinding is on when ANY group resolves references — a
			// mapping-level baseline can enable it even without an env baseline.
			anyRB := false
			for _, g := range groups {
				if g.rb != nil {
					anyRB = true
					break
				}
			}
			if outcomes := deploy.RebindReports(client, token, modelsByWS, pending, anyRB); len(outcomes) > 0 {
				allResults = foldRebindOutcomes(allResults, outcomes)
			}
		}
	}

	// Deletes are confirmed and run SEPARATELY — never on the deploy "yes".
	if nDel > 0 {
		// Name the actual target workspace(s), not just the env alias, so the user
		// sees exactly where items will be irreversibly removed.
		var wsNames []string
		seenWS := map[string]bool{}
		for i, g := range groups {
			if len(deletesByGroup[i]) > 0 && !seenWS[g.Target.DisplayName] {
				seenWS[g.Target.DisplayName] = true
				wsNames = append(wsNames, g.Target.DisplayName)
			}
		}
		ok, derr := confirm(fmt.Sprintf("⚠ DELETE %d item(s) from %s? This is irreversible.", nDel, strings.Join(wsNames, ", ")))
		if derr != nil {
			return allResults, derr
		}
		if !ok {
			fmt.Println("Deletes skipped.")
			return allResults, nil
		}
		for i, g := range groups {
			dels := deletesByGroup[i]
			if len(dels) == 0 {
				continue
			}
			sp := ui.NewSpinner(fmt.Sprintf("Deleting from %s...", g.Target.DisplayName))
			sp.Start()
			allResults = append(allResults, deploy.DeleteItems(client, token, g.Target, dels)...)
			sp.Stop()
		}
	}
	return allResults, nil
}

// runBulkPublish publishes selected items using the bulk-import backend. The
// bulkImportDefinitions API is workspace-scoped, so items from groups that share
// a target workspace are merged into ONE call per distinct workspace (this also
// lets Fabric resolve cross-item byPath references within a single payload). One
// LRO per workspace, so the spinner shows a count + the live throttle countdown
// rather than per-item progress.
func runBulkPublish(client deploy.FabricClient, token string, groups []deployGroup, selected map[int][]deploy.LocalItem) ([]deploy.Result, error) {
	type bucket struct {
		target fabric.Workspace
		items  []deploy.LocalItem
		rb     *deploy.Rebinder
	}
	buckets := map[string]*bucket{}
	var order []string
	for i, g := range groups {
		items := selected[i]
		if len(items) == 0 {
			continue
		}
		// Merge by workspace AND rebinder: groups sharing a target normally share
		// the env-level rebinder (one call, cross-item byPath resolution intact),
		// but a mapping with its own baseline must not have its items rebound
		// through another mapping's index.
		key := fmt.Sprintf("%s/%p", g.Target.ID, g.rb)
		b := buckets[key]
		if b == nil {
			b = &bucket{target: g.Target, rb: g.rb}
			buckets[key] = b
			order = append(order, key)
		}
		b.items = append(b.items, items...)
	}

	var results []deploy.Result
	for _, id := range order {
		b := buckets[id]
		render := func() string {
			return fmt.Sprintf("Bulk importing %d item(s) to %s", len(b.items), b.target.DisplayName) + liveThrottleStatus()
		}
		sp := ui.NewSpinner(render())
		sp.SetMessageFunc(render)
		sp.Start()
		r, err := deploy.BulkImport(client, token, b.target, b.items, b.rb)
		sp.Stop()
		results = append(results, r...)
		if err != nil {
			return results, fmt.Errorf("bulk import to %s: %w", b.target.DisplayName, err)
		}
	}
	return results, nil
}

// targetsSummary lists the distinct target workspace names for the compare header.
func targetsSummary(groups []deployGroup) string {
	seen := map[string]bool{}
	var names []string
	for _, g := range groups {
		if !seen[g.Target.DisplayName] {
			seen[g.Target.DisplayName] = true
			names = append(names, g.Target.DisplayName)
		}
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// saveDeployHistory writes an HTML deploy-report to the customer's configured
// repo-relative history folder. It's a no-op when nothing was published (empty
// results — e.g. the user continued past the gate but selected nothing or
// cancelled the inner confirm), so aborted runs don't litter the history with
// empty reports. When items WERE deployed but no history folder is configured
// it offers (once per run) to set the default docs/deploys and record this very
// deploy; declining prints the old skip notice. The report's diff section reflects every compared item
// while the results section reflects what was actually deployed (a cherry-picked
// subset shows fewer results than diffs). A write failure is non-fatal — the
// deploy already happened.
func saveDeployHistory(configPath, customerName string, customer config.Customer, groups []deployGroup, results []deploy.Result, postRuns []postDeployOutcome, confirm func(string) (bool, error)) {
	if len(results) == 0 {
		return // nothing was published — no report to write
	}
	if customer.RepoPath == "" && customer.DeployHistoryPath == "" {
		// A relative DeployHistoryPath is anchored to the primary repo; without
		// one there's nothing to anchor to — nothing to offer either.
		return
	}
	if customer.DeployHistoryPath == "" {
		// First deploy without history configured: offer to set it NOW, so this
		// very run gets a report — the moment it's most wanted and most easily
		// forgotten. Declining keeps the old skip notice.
		ok, err := confirm(fmt.Sprintf("No deploy-history folder set — save a report per deploy to %s/docs/deploys from now on (including this run)?", filepath.Base(customer.RepoPath)))
		if err != nil || !ok {
			fmt.Println(infoStyle.Render("No deploy-history folder set — skipping report. Set one with `futils edit`."))
			return
		}
		customer.DeployHistoryPath = "docs/deploys"
		if serr := config.EditCustomer(configPath, customerName, customer); serr != nil {
			fmt.Println(warningStyle.Render("Couldn't save deploy-history setting: " + serr.Error()))
		}
	}
	if customer.RepoPath == "" {
		// A relative DeployHistoryPath is anchored to the primary repo; without one
		// there's nothing to anchor to, so the report is skipped even though a path is set.
		fmt.Println(infoStyle.Render("Deploy history needs a primary repo to anchor relative paths — this customer has none, so skipping report."))
		return
	}
	// Relative history is anchored to the PRIMARY repo (customer.RepoPath) even
	// when a deploy spans multiple repos; an absolute DeployHistoryPath is used verbatim.
	dir := historyDir(customer.RepoPath, customer.DeployHistoryPath)
	ts := time.Now()
	htmlDoc := renderDeployReport(groups, results, postRuns, ts)
	path, err := writeHistoryReport(dir, ts, htmlDoc)
	if err != nil {
		fmt.Println(warningStyle.Render("Couldn't save deploy report: " + err.Error()))
		return
	}
	fmt.Println(infoStyle.Render("Saved deploy report: ") + terminalLink("file://"+path, path))
}

// historyDir resolves the deploy-history folder. A relative DeployHistoryPath is
// joined onto the repo; an absolute one is used as-is (joining two absolute
// paths previously produced a doubled repo/repo/file path).
func historyDir(repoPath, histPath string) string {
	if filepath.IsAbs(histPath) {
		return histPath
	}
	return filepath.Join(repoPath, histPath)
}

// terminalLink wraps label in an OSC 8 hyperlink to url so terminals that
// support it (ghostty/cmux, iTerm2, …) make the text clickable. Terminals that
// don't render the escapes as a no-op and show the label plainly.
func terminalLink(url, label string) string {
	return "\x1b]8;;" + url + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// pickEnvironment shows the customer's environment aliases as a numbered menu.
// promptFirstRunBaseline asks which environment the repo's baked GUIDs belong
// to, shown on the first deploy of a customer without a baseline. The cursor
// starts on the first "dev"-looking alias (the answer in practice), and an
// explicit Skip deploys without auto-rebind like before. Esc backs out of the
// deploy.
func promptFirstRunBaseline(customer config.Customer) (string, error) {
	const skipValue = "\x00skip"
	fmt.Println()
	fmt.Println(infoStyle.Render("Baseline environment — which environment does the git code belong to?"))
	fmt.Println(wrapIndented("Auto-rebind reads the GUIDs in git as belonging to this environment and swaps them to the target's on deploy. Without a baseline, references (lakehouse GUIDs, SQL endpoints) deploy unchanged — still pointing at the environment they were developed in.", 2))
	preselected := false
	opts := make([]ui.MenuOption, 0, len(customer.Environments)+1)
	for _, e := range customer.Environments {
		pre := !preselected && strings.Contains(strings.ToLower(e.Alias), "dev")
		if pre {
			preselected = true
		}
		opts = append(opts, ui.MenuOption{
			Label:     e.Alias,
			Value:     e.Alias,
			Preselect: pre,
			Info:      "Baseline is the environment your repo represents. futils reads the GUIDs in git as baseline GUIDs, resolves them by name, and swaps to the target environment's GUIDs on deploy.",
		})
	}
	opts = append(opts, ui.MenuOption{
		Label:       "Skip — deploy without auto-rebind",
		Value:       skipValue,
		Description: "References deploy unchanged into the target. You can set the baseline later via Manage customers → Edit customer.",
	})
	choice, err := ui.NumberMenu("Which environment do the repo's GUIDs belong to (baseline)?", opts)
	if err != nil {
		return "", err
	}
	if choice == skipValue {
		return "", nil
	}
	return choice, nil
}

func pickEnvironment(customer config.Customer) (string, error) {
	options := make([]ui.MenuOption, len(customer.Environments))
	for i, e := range customer.Environments {
		label := e.Alias
		if len(e.Deployments) > 0 {
			label = fmt.Sprintf("%s (%d mapping%s)", e.Alias, len(e.Deployments), pluralS(len(e.Deployments)))
		}
		options[i] = ui.MenuOption{Label: label, Value: e.Alias}
	}
	return ui.NumberMenu("Select environment to deploy to", options)
}

// resolveWorkspaceByName finds a workspace by display name among those the user
// can see, with an actionable error if it's missing.
func resolveWorkspaceByName(workspaces []fabric.Workspace, name string) (fabric.Workspace, error) {
	for _, w := range workspaces {
		if w.DisplayName == name {
			return w, nil
		}
	}
	return fabric.Workspace{}, fmt.Errorf("workspace %q not found (check spelling and your access)", name)
}

// pickGroupedRows shows all groups' deployable rows (New/Changed/Exists, colored
// by class) as one unchecked checkbox list, sorted by class. Orphan rows appear
// as red skip-bulk-select rows. Returns chosen LocalItems keyed by group index
// (deploys) and chosen DeleteTargets keyed by group index (deletes).
func pickGroupedRows(groups []deployGroup) (map[int][]deploy.LocalItem, map[int][]deploy.DeleteTarget, error) {
	items, entries, title := buildDeployPickRows(groups)
	if len(items) == 0 {
		return map[int][]deploy.LocalItem{}, nil, nil
	}
	idx, err := ui.MultiSelectRich(title, items)
	if err != nil {
		return nil, nil, err
	}
	deploys := map[int][]deploy.LocalItem{}
	deletes := map[int][]deploy.DeleteTarget{}
	for _, k := range idx {
		e := entries[k]
		if e.delete != nil {
			deletes[e.gi] = append(deletes[e.gi], *e.delete)
		} else {
			deploys[e.gi] = append(deploys[e.gi], e.item)
		}
	}
	return deploys, deletes, nil
}

// pickEntry is the identity behind one picker CheckItem: which group and which
// local item. Index-aligned with the CheckItem slice, so identical labels never
// collide (the old label-keyed map silently dropped duplicates).
type pickEntry struct {
	gi     int
	item   deploy.LocalItem     // set for a deploy row
	delete *deploy.DeleteTarget // non-nil for an orphan delete row
}

// classRank orders the picker: New first, then Changed, then Exists (unverified),
// then Orphan (the selectable-for-deletion rows) last. Unchanged never reaches the
// picker. Each rank is explicit so the default catches only genuinely-unranked
// classes (sorted last) rather than silently interleaving them with Orphan.
func classRank(c deploy.Class) int {
	switch c {
	case deploy.ClassNew:
		return 0
	case deploy.ClassChanged:
		return 1
	case deploy.ClassExists:
		return 2
	case deploy.ClassOrphan:
		return 3
	default:
		return 99
	}
}

// classLegend renders the given classes' names, each in its class color, joined
// with " · " — the picker's color key.
func classLegend(classes []deploy.Class) string {
	parts := make([]string, 0, len(classes))
	for _, c := range classes {
		parts = append(parts, classStyle(c).Render(c.String()))
	}
	return strings.Join(parts, " · ")
}

// buildDeployPickRows turns the compare groups into the picker's rows: New/Changed/
// Exists items to deploy plus Orphan items to delete (only Unchanged is filtered
// out), sorted by class → type → name, each colored by classStyle and unchecked by
// default. Orphan rows are SkipBulkSelect (select-all never marks a delete) and carry
// a DeleteTarget on their entry. Returns the CheckItems, an index-aligned entry slice
// (CheckItem k ↔ entry k), and the picker title (with a color legend). With a single
// target workspace the workspace is named in the title; with multiple, each row carries
// a " → <ws>" suffix (inherits the row's class color, since the whole label is class-styled).
func buildDeployPickRows(groups []deployGroup) ([]ui.CheckItem, []pickEntry, string) {
	type row struct {
		gi         int
		class      deploy.Class
		typ        string
		name       string
		target     string
		item       deploy.LocalItem
		deployedID string
	}
	var rows []row
	targets := map[string]bool{}
	for gi, g := range groups {
		for _, r := range g.Rows {
			if r.Class == deploy.ClassUnchanged {
				continue
			}
			rows = append(rows, row{gi, r.Class, r.ItemType(), r.Name(), g.Target.DisplayName, r.Local, r.DeployedID})
			targets[g.Target.DisplayName] = true
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := classRank(rows[i].class), classRank(rows[j].class); ri != rj {
			return ri < rj
		}
		if rows[i].typ != rows[j].typ {
			return rows[i].typ < rows[j].typ
		}
		return rows[i].name < rows[j].name
	})

	multiTarget := len(targets) > 1
	items := make([]ui.CheckItem, len(rows))
	entries := make([]pickEntry, len(rows))
	seen := map[deploy.Class]bool{}
	for k, r := range rows {
		label := fmt.Sprintf("%-14s %s", r.typ, r.name)
		if multiTarget {
			label += " → " + r.target
		}
		isOrphan := r.class == deploy.ClassOrphan
		items[k] = ui.CheckItem{Label: label, Style: classStyle(r.class), Checked: false, SkipBulkSelect: isOrphan}
		if isOrphan {
			entries[k] = pickEntry{gi: r.gi, delete: &deploy.DeleteTarget{ID: r.deployedID, Name: r.name, Type: r.typ}}
		} else {
			entries[k] = pickEntry{gi: r.gi, item: r.item}
		}
		seen[r.class] = true
	}

	title := "Select items to deploy"
	if len(targets) == 1 {
		for t := range targets {
			title += " to " + t
		}
	}
	var present []deploy.Class
	for _, c := range []deploy.Class{deploy.ClassNew, deploy.ClassChanged, deploy.ClassExists, deploy.ClassOrphan} {
		if seen[c] {
			present = append(present, c)
		}
	}
	if len(present) > 0 {
		title += "\n  " + classLegend(present)
	}
	return items, entries, title
}

// Pre-built per-class styles — package-level so render loops don't re-allocate
// a lipgloss.Style on every row. Colors match the originals exactly.
var (
	classStyleNew       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	classStyleChanged   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	classStyleUnchanged = lipgloss.NewStyle().Foreground(ui.DimColor)
	classStyleOrphan    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	classStyleExists    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// classStyle colors a compare row by its classification: green=new,
// yellow=changed, grey=unchanged, red=orphan, cyan=exists-but-unverified.
func classStyle(c deploy.Class) lipgloss.Style {
	switch c {
	case deploy.ClassNew:
		return classStyleNew
	case deploy.ClassChanged:
		return classStyleChanged
	case deploy.ClassUnchanged:
		return classStyleUnchanged
	case deploy.ClassOrphan:
		return classStyleOrphan
	default: // ClassExists (unverified)
		return classStyleExists
	}
}

// countByClass tallies compare rows by classification across all groups.
func countByClass(groups []deployGroup) map[deploy.Class]int {
	c := map[deploy.Class]int{}
	for _, g := range groups {
		for _, r := range g.Rows {
			c[r.Class]++
		}
	}
	return c
}

// printGroupedCompare renders the compare result grouped by target workspace,
// colored by classification with a legend.
func printGroupedCompare(groups []deployGroup) {
	counts := countByClass(groups)
	order := []deploy.Class{deploy.ClassNew, deploy.ClassChanged, deploy.ClassUnchanged, deploy.ClassExists, deploy.ClassOrphan}
	var parts []string
	for _, cl := range order {
		if n := counts[cl]; n > 0 {
			parts = append(parts, classStyle(cl).Render(fmt.Sprintf("%d %s", n, cl)))
		}
	}
	fmt.Println()
	if len(parts) > 0 {
		fmt.Println("  " + strings.Join(parts, "  ·  "))
	}
	legend := classStyle(deploy.ClassNew).Render("New") + "  " +
		classStyle(deploy.ClassChanged).Render("Changed") + "  " +
		classStyle(deploy.ClassUnchanged).Render("Unchanged") + "  " +
		classStyle(deploy.ClassOrphan).Render("Orphan") + "  " +
		classStyle(deploy.ClassExists).Render("Exists")
	fmt.Println("  " + legend)
	fmt.Println()
	for _, g := range groups {
		header := g.Target.DisplayName
		if g.Folder != "" {
			header = g.Folder + "/ → " + g.Target.DisplayName
		}
		fmt.Println(infoStyle.Render(header))
		if len(g.Rows) == 0 {
			fmt.Println("  (no items)")
			continue
		}
		// Sort a display copy by class (New → Changed → Exists → Orphan) then
		// type then name — matching the picker. The class is conveyed by color
		// (the legend above is the key), so the per-row class word is dropped.
		rows := append([]deploy.CompareRow(nil), g.Rows...)
		sort.SliceStable(rows, func(i, j int) bool {
			if ri, rj := classRank(rows[i].Class), classRank(rows[j].Class); ri != rj {
				return ri < rj
			}
			if rows[i].ItemType() != rows[j].ItemType() {
				return rows[i].ItemType() < rows[j].ItemType()
			}
			return rows[i].Name() < rows[j].Name()
		})
		for _, r := range rows {
			if r.Class == deploy.ClassUnchanged {
				continue // counted in the summary; not worth a per-row line
			}
			line := fmt.Sprintf("  %-14s %s", r.ItemType(), r.Name())
			fmt.Println(classStyle(r.Class).Render(line))
		}
	}
	fmt.Println()
}

// printRebindSummary lists every reference rewrite the rebinder will apply,
// deduplicated by (Kind, Old, New) across the whole run — one line per unique
// change, not per item. Silent when nothing changes.
func printRebindSummary(groups []deployGroup) {
	type key struct{ kind, old, new string }
	seen := map[key]bool{}
	var ordered []deploy.RebindChange
	for _, g := range groups {
		for _, c := range g.Changes {
			k := key{c.Kind, c.Old, c.New}
			if seen[k] {
				continue
			}
			seen[k] = true
			ordered = append(ordered, c)
		}
	}
	if len(ordered) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(infoStyle.Render(fmt.Sprintf("%d reference(s) will be rebound baseline → target:", len(ordered))))
	// Group by (Kind, Name) so an item whose rewrite spans several values — a
	// SQL endpoint rebinds both its host and its id — reads as ONE reference
	// with its rewrites indented beneath, not as unrelated rows.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].Name < ordered[j].Name
	})
	lastKind, lastName := "", ""
	for _, c := range ordered {
		if c.Kind != lastKind || c.Name != lastName {
			fmt.Printf("  %-12s %s\n", c.Kind, c.Name)
			lastKind, lastName = c.Kind, c.Name
		}
		fmt.Printf("    %s → %s\n", c.Old, c.New)
	}
	fmt.Println()
}

// printReportBindings lists each report→semantic-model binding the deploy will
// apply, so the user sees which model a report binds to (and in which target
// workspace) before the deploy gate. Silent when no report binds.
func printReportBindings(groups []deployGroup) {
	var all []deploy.ReportBinding
	seen := map[string]bool{}
	for _, g := range groups {
		for _, b := range g.ReportBindings {
			k := b.Report + "\x00" + b.Model + "\x00" + b.Workspace
			if seen[k] {
				continue
			}
			seen[k] = true
			all = append(all, b)
		}
	}
	if len(all) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(infoStyle.Render("Report bindings:"))
	for _, b := range all {
		fmt.Printf("  %-24s →  %s  (%s)\n", b.Report, b.Model, b.Workspace)
	}
	fmt.Println()
}

// printUnresolved lists reference GUIDs the rebinder could not translate, with
// enough context for the user to register an override (or ignore/strip them),
// followed by a likely-causes box tailored to the reasons that actually
// occurred — the by-far most common cause is a workspace (typically a
// reference-only Data workspace) missing from the baseline or target
// environment's workspace list, which no amount of overrides fixes properly.
// Silent when everything resolved.
func printUnresolved(groups []deployGroup, baselineAlias, targetAlias string) {
	var total int
	reasons := map[string]bool{}
	for _, g := range groups {
		total += len(g.Unresolved)
		for _, u := range g.Unresolved {
			reasons[u.Reason] = true
		}
	}
	if total == 0 {
		return
	}
	fmt.Println()
	fmt.Println(warningStyle.Render(fmt.Sprintf("%d unresolved reference(s) — left as-is. Register an override (Edit customer) to map them by name:", total)))
	for _, g := range groups {
		for _, u := range g.Unresolved {
			fmt.Printf("  %s in %s — looks like a %s (%s): %s%s\n", shortGUID(u.GUID), u.ItemName, u.ItemType, u.Location, reasonText(u.Reason), countSuffix(u.Count))
		}
	}
	var hints []string
	if reasons[deploy.ReasonNameUnknown] {
		env := "the baseline environment"
		if baselineAlias != "" {
			env = fmt.Sprintf("baseline environment %q", baselineAlias)
		}
		hints = append(hints, fmt.Sprintf("GUID not found in any baseline workspace: the item probably lives in a workspace that isn't registered on %s — reference-only workspaces (e.g. a Data workspace you never deploy to) must be added too. Fix: Edit customer → Edit %s → Add workspace, then redeploy.", env, orAlias(baselineAlias, "<baseline env>")))
	}
	if reasons[deploy.ReasonNotInTarget] {
		hints = append(hints, fmt.Sprintf("no same-named item in the target workspaces: the name resolved in the baseline, but env %q has no workspace containing an item with that name — check that the counterpart workspace is registered on the target environment (and that the item exists there).", targetAlias))
	}
	if reasons[deploy.ReasonAmbiguous] {
		hints = append(hints, fmt.Sprintf("name matches items in several target workspaces: the same name+type exists in more than one of env %q's workspaces, so name-matching is unsafe — resolve it with a reference override, or give the mapping a dedicated baseline workspace to scope the lookup.", targetAlias))
	}
	if len(hints) > 0 {
		fmt.Println()
		fmt.Println(infoStyle.Render("Likely causes:"))
		for _, h := range hints {
			fmt.Println(wrapIndented("• "+h, 2))
		}
	}
	fmt.Println()
}

// orAlias returns the alias, or a placeholder when it is empty.
func orAlias(alias, placeholder string) string {
	if alias == "" {
		return placeholder
	}
	return alias
}

// countSuffix renders how many occurrences collapsed into one unresolved ref —
// " (×80)" — and nothing for a single occurrence.
func countSuffix(n int) string {
	if n > 1 {
		return fmt.Sprintf(" (×%d)", n)
	}
	return ""
}

// reasonText renders an UnresolvedRef reason code as a short human hint, so the
// user can tell a baseline-index miss from a target-side failure without
// guessing.
func reasonText(reason string) string {
	switch reason {
	case deploy.ReasonNameUnknown:
		return "GUID not found in any baseline workspace"
	case deploy.ReasonNotInTarget:
		return "no same-named item in the target workspaces"
	case deploy.ReasonAmbiguous:
		return "name matches items in several target workspaces"
	case "":
		return "unknown reason"
	}
	return reason
}

// pickDeployScope lets the user deploy a single folder→workspace mapping or all
// of them. With one mapping it's a no-op. Returns the chosen subset.
func pickDeployScope(mappings []config.DeployMapping) ([]config.DeployMapping, error) {
	if len(mappings) <= 1 {
		return mappings, nil
	}
	opts := []ui.MenuOption{{Label: fmt.Sprintf("All (%d mappings)", len(mappings)), Value: "__all"}}
	for i, m := range mappings {
		opts = append(opts, ui.MenuOption{Label: fmt.Sprintf("%s → %s", mappingLabel(m.Folder, m.Repo), m.Workspace), Value: fmt.Sprintf("%d", i)})
	}
	choice, err := ui.NumberMenu("Deploy which folder?", opts)
	if err != nil {
		return nil, err
	}
	if choice == "__all" {
		return mappings, nil
	}
	idx, err := strconv.Atoi(choice)
	if err != nil || idx < 0 || idx >= len(mappings) {
		return nil, fmt.Errorf("invalid selection %q", choice)
	}
	return []config.DeployMapping{mappings[idx]}, nil
}

// mapUnresolvedInteractive walks the user through each unresolved reference,
// offering register / override / ignore / skip, and persists the chosen
// mutations. Returns after saving; the user re-runs the deploy to apply.
func mapUnresolvedInteractive(client APIClient, token, configPath, customerName string, customer config.Customer, refs []deploy.UnresolvedRef) error {
	changed := false
	for _, ref := range refs {
		fmt.Printf("\n%s in %s — looks like a %s (%s): %s%s\n", shortGUID(ref.GUID), ref.ItemName, ref.ItemType, ref.Location, reasonText(ref.Reason), countSuffix(ref.Count))
		choice, err := ui.NumberMenu("How do you want to resolve it?", refActionOptions(ref))
		if err != nil {
			if errors.Is(err, ui.ErrGoBack) {
				break
			}
			return err
		}
		var action RefAction
		switch choice {
		case refActionSkip:
			continue
		case refActionIgnore:
			action = RefAction{Kind: refActionIgnore}
		case refActionRegister:
			ws, env, perr := pickWorkspaceAndEnv(client, token, customer)
			if perr != nil {
				if errors.Is(perr, ui.ErrGoBack) {
					continue
				}
				return perr
			}
			action = RefAction{Kind: refActionRegister, EnvAlias: env, Workspace: ws}
		case refActionOverride:
			itemType, itemName, perr := pickTargetItem(client, token, ref.ItemType)
			if perr != nil {
				if errors.Is(perr, ui.ErrGoBack) {
					continue
				}
				return perr
			}
			action = RefAction{Kind: refActionOverride, ItemType: itemType, ItemName: itemName}
		}
		customer = applyRefAction(customer, ref, action)
		changed = true
	}
	if !changed {
		return nil
	}
	if err := config.EditCustomer(configPath, customerName, customer); err != nil {
		return fmt.Errorf("save reference mappings: %w", err)
	}
	fmt.Println(infoStyle.Render("Saved. Re-run the deploy to apply the new mappings."))
	return nil
}

// pickWorkspaceAndEnv lets the user pick any visible workspace and which env to
// register it on, for the "register reference workspace" action.
func pickWorkspaceAndEnv(client APIClient, token string, customer config.Customer) (workspace, envAlias string, err error) {
	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return "", "", fmt.Errorf("list workspaces: %w", err)
	}
	wsOpts := make([]ui.FilterOption, 0, len(workspaces))
	for _, w := range workspaces {
		wsOpts = append(wsOpts, ui.FilterOption{Label: w.DisplayName, Value: w.DisplayName})
	}
	workspace, err = ui.FilterMenu("Which workspace does it live in?", wsOpts, ui.DefaultFilterRowRenderer)
	if err != nil {
		return "", "", err
	}
	envOpts := make([]ui.MenuOption, len(customer.Environments))
	for i, e := range customer.Environments {
		envOpts[i] = ui.MenuOption{Label: e.Alias, Value: e.Alias}
	}
	envAlias, err = ui.NumberMenu("Register it on which environment?", envOpts)
	if err != nil {
		return "", "", err
	}
	return workspace, envAlias, nil
}

// pickTargetItem lets the user pick a target workspace then an item of the
// given type in it, for the "override" action. Returns (itemType, itemName).
func pickTargetItem(client APIClient, token string, itemType string) (string, string, error) {
	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return "", "", fmt.Errorf("list workspaces: %w", err)
	}
	wsOpts := make([]ui.FilterOption, 0, len(workspaces))
	wsByName := map[string]fabric.Workspace{}
	for _, w := range workspaces {
		wsOpts = append(wsOpts, ui.FilterOption{Label: w.DisplayName, Value: w.DisplayName})
		wsByName[w.DisplayName] = w
	}
	wsName, err := ui.FilterMenu("Pick the target workspace", wsOpts, ui.DefaultFilterRowRenderer)
	if err != nil {
		return "", "", err
	}
	items, err := client.ListItems(token, wsByName[wsName].ID)
	if err != nil {
		return "", "", fmt.Errorf("list items: %w", err)
	}
	itemOpts := make([]ui.FilterOption, 0, len(items))
	for _, it := range items {
		if itemType == "" || it.Type == itemType {
			itemOpts = append(itemOpts, ui.FilterOption{Label: fmt.Sprintf("%s (%s)", it.DisplayName, it.Type), Value: it.DisplayName + "\x00" + it.Type})
		}
	}
	if len(itemOpts) == 0 {
		fmt.Println("No items of that type in the chosen workspace.")
		return "", "", ui.ErrGoBack
	}
	chosen, err := ui.FilterMenu("Pick the item to map to", itemOpts, ui.DefaultFilterRowRenderer)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(chosen, "\x00", 2)
	return parts[1], parts[0], nil // itemType, itemName
}

// appendWarning appends addition to existing warning, joining with "; " when
// existing is non-empty. Prevents clobbering a pre-existing Warning (e.g. a
// description-sync warning) when a later rebind step adds its own.
func appendWarning(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

// foldRebindOutcomes merges the post-deploy report-rebind outcomes back into
// allResults, matched by deployed GUID. Each outcome either sets an error on
// the corresponding Result or appends a warning (preserving any pre-existing
// warning from the deploy pass). Items whose GUID isn't in allResults are
// silently skipped (they were not in this run's cherry-pick set).
func foldRebindOutcomes(results []deploy.Result, outcomes []deploy.ReportRebindOutcome) []deploy.Result {
	byID := map[string]*deploy.Result{}
	for i := range results {
		if results[i].ID != "" {
			byID[results[i].ID] = &results[i]
		}
	}
	for _, o := range outcomes {
		r, ok := byID[o.ReportID]
		if !ok {
			continue
		}
		if o.Err != nil {
			r.Err = o.Err
		}
		if o.Warning != "" {
			r.Warning = appendWarning(r.Warning, o.Warning)
		}
	}
	return results
}

func printDeployResults(results []deploy.Result) {
	if len(results) == 0 {
		return
	}
	// Deletes are counted separately from deploys so the headline never claims a
	// deleted item was "deployed" (it never reached the workspace as content).
	var deployed, deleted, failed, warned int
	var b string
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed++
			b += fmt.Sprintf("  ✗ %s (%s): %v\n", r.Name, r.Type, r.Err)
			if r.Warning != "" {
				warned++
				b += fmt.Sprintf("  ⚠ %s (%s): %s\n", r.Name, r.Type, r.Warning)
			}
		case r.Action == deploy.ActionDelete:
			deleted++
			b += fmt.Sprintf("  ✓ %s (%s) %s\n", r.Name, r.Type, r.Action)
		case r.Warning != "":
			deployed++
			warned++
			b += fmt.Sprintf("  ⚠ %s (%s) %s — %s\n", r.Name, r.Type, r.Action, r.Warning)
		default:
			deployed++
			b += fmt.Sprintf("  ✓ %s (%s) %s\n", r.Name, r.Type, r.Action)
		}
	}
	summary := fmt.Sprintf("Deployed %d item(s)", deployed)
	if deleted > 0 {
		summary += fmt.Sprintf(", deleted %d", deleted)
	}
	if warned > 0 {
		summary += fmt.Sprintf(", %d with warnings", warned)
	}
	fmt.Println()
	if failed > 0 {
		fmt.Println(warningStyle.Render(fmt.Sprintf("%s, %d failure(s)\n%s", summary, failed, b)))
	} else {
		fmt.Println(successStyle.Render(summary + "\n" + b))
	}
}

// envPublishPollInterval / envPublishMaxPolls pace the environment publish
// wait: staging→publish resolves libraries server-side and commonly takes
// single-digit minutes; 10s × 180 caps the wait at 30 minutes. Vars so tests
// can collapse them.
var (
	envPublishPollInterval = 10 * time.Second
	envPublishMaxPolls     = 180
)

// publishEnvironments submits staging→publish for every successfully deployed
// Environment item, then waits for the publishes to settle. Deploying an
// Environment's definition only STAGES sparkcompute/libraries — nothing takes
// effect until the environment-specific publish API runs (mirrors
// fabric-cicd). Because a publish can take many minutes on a real tenant, the
// user confirms first; declining leaves the staged state for a manual publish
// in the portal. All failures are warnings — the definitions deployed fine.
func publishEnvironments(client APIClient, token string, results []deploy.Result, confirm func(string) (bool, error)) {
	var envs []deploy.Result
	for _, res := range results {
		if res.Type == "Environment" && res.Action != deploy.ActionDelete && res.Err == nil && res.ID != "" {
			envs = append(envs, res)
		}
	}
	if len(envs) == 0 {
		return
	}

	ok, err := confirm(fmt.Sprintf(
		"Publish %d environment(s) now? Deployed settings/libraries are only staged until published — publishing can take several minutes.",
		len(envs)))
	if err != nil || !ok {
		fmt.Println(infoStyle.Render("Environments left staged — publish them from the workspace when ready."))
		return
	}

	// Submit every publish first (fire-and-forget), then watch them together.
	pending := map[string]deploy.Result{} // itemID -> result
	for _, e := range envs {
		if err := client.PublishEnvironment(token, e.WorkspaceID, e.ID); err != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: publish not submitted: %v", e.Name, err)))
			continue
		}
		pending[e.ID] = e
	}
	if len(pending) == 0 {
		return
	}

	total := len(pending)
	var settled int64
	sp := ui.NewSpinner("")
	sp.SetMessageFunc(func() string {
		return fmt.Sprintf("Publishing %d environment(s)… %d/%d done", total, atomic.LoadInt64(&settled), total)
	})
	sp.Start()
	var failures []string
	for i := 0; i < envPublishMaxPolls && len(pending) > 0; i++ {
		if i > 0 {
			time.Sleep(envPublishPollInterval)
		}
		for id, e := range pending {
			state, err := client.GetEnvironmentPublishState(token, e.WorkspaceID, id)
			if err != nil {
				continue // transient read failure — poll again next round
			}
			switch strings.ToLower(state) {
			case "success":
				delete(pending, id)
			case "failed", "cancelled":
				failures = append(failures, fmt.Sprintf("%s: publish %s — check the environment in its workspace", e.Name, strings.ToLower(state)))
				delete(pending, id)
			}
		}
		atomic.StoreInt64(&settled, int64(total-len(pending)))
	}
	sp.Stop()

	for _, e := range pending {
		failures = append(failures, fmt.Sprintf("%s: publish still running after %s — it continues server-side; check the workspace later",
			e.Name, time.Duration(envPublishMaxPolls)*envPublishPollInterval))
	}
	sort.Strings(failures)
	for _, f := range failures {
		fmt.Println(warningStyle.Render(f))
	}
	if succeeded := total - len(failures); len(failures) == 0 {
		fmt.Println(infoStyle.Render(fmt.Sprintf("%d environment(s) published.", total)))
	} else if succeeded > 0 {
		fmt.Println(infoStyle.Render(fmt.Sprintf("%d of %d environment(s) published.", succeeded, total)))
	}
}

// activateVariableLibraries enforces the value-set-per-environment convention
// after a publish, mirroring fabric-cicd: every successfully deployed
// VariableLibrary whose settings.json lists a value set named after the target
// environment gets that set activated in the target workspace. The active set
// is workspace state (never part of the definition), so with no matching set
// the target's choice is deliberately left alone — with a hint, not an error.
func activateVariableLibraries(client APIClient, token, alias string, groups []deployGroup, results []deploy.Result) {
	valueSets := map[string][]string{} // display name -> valueSetsOrder from the local settings.json
	for _, g := range groups {
		for _, r := range g.Rows {
			if r.Local.Type != "VariableLibrary" {
				continue
			}
			for _, p := range r.Local.Parts {
				if path.Base(p.Path) != "settings.json" {
					continue
				}
				var settings struct {
					ValueSetsOrder []string `json:"valueSetsOrder"`
				}
				if json.Unmarshal(p.Content, &settings) == nil {
					valueSets[r.Local.DisplayName] = settings.ValueSetsOrder
				}
			}
		}
	}
	if len(valueSets) == 0 {
		return
	}
	for _, res := range results {
		if res.Type != "VariableLibrary" || res.Action == deploy.ActionDelete || res.Err != nil || res.ID == "" {
			continue
		}
		order, ok := valueSets[res.Name]
		if !ok {
			continue
		}
		match := false
		for _, name := range order {
			if name == alias {
				match = true
				break
			}
		}
		if !match {
			fmt.Println(infoStyle.Render(fmt.Sprintf(
				"%s: no value set named %q — the target's active value set is unchanged. Name a value set after the environment to have deploys activate it.", res.Name, alias)))
			continue
		}
		if err := client.SetVariableLibraryActiveSet(token, res.WorkspaceID, res.ID, alias); err != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: couldn't activate value set %q: %v", res.Name, alias, err)))
			continue
		}
		fmt.Println(infoStyle.Render(fmt.Sprintf("%s: active value set → %s", res.Name, alias)))
	}
}
