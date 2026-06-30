package cmd

import (
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
		return fmt.Errorf("customer %q has no environments — add one via Edit customer first", customerName)
	}

	repoPath := customer.RepoPath
	pickedRepo := repoPath == ""
	if pickedRepo {
		startDir, _ := os.UserHomeDir()
		repoPath, err = ui.PickDirectory("Select the Fabric git repo (enter to choose the highlighted folder)", startDir)
		if err != nil {
			return err
		}
	}

	src, err := deploy.NewSource(repoPath)
	if err != nil {
		return err
	}
	// Remember the repo on the customer so the picker is skipped next time.
	if pickedRepo {
		customer.RepoPath = src.Repo()
		if err := config.EditCustomer(configPath, customerName, customer); err != nil {
			fmt.Println(warningStyle.Render("Couldn't save repo path: " + err.Error()))
		} else {
			fmt.Println(infoStyle.Render(fmt.Sprintf("Saved repo path for %s: %s", customerName, src.Repo())))
		}
	}
	sp := ui.NewSpinner(fmt.Sprintf("Fetching and reading %s...", src.Ref()))
	sp.Start()
	var all []deploy.LocalItem
	var fetchErr, discErr error
	func() {
		defer sp.Stop()
		if fetchErr = src.Fetch(); fetchErr != nil {
			return
		}
		all, discErr = src.DiscoverItems()
	}()
	if fetchErr != nil {
		return fetchErr
	}
	if discErr != nil {
		return discErr
	}
	if len(all) == 0 {
		return fmt.Errorf("no Fabric items found at %s", src.Ref())
	}

	alias, err := pickEnvironment(customer)
	if err != nil {
		return err
	}

	token, err := client.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	workspaces, err := client.ListWorkspaces(token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	rebinder, err := buildRebinder(client, token, customer, alias, workspaces)
	if err != nil {
		return fmt.Errorf("set up reference rebinding: %w", err)
	}
	if customer.BaselineEnvironment == "" {
		fmt.Println(infoStyle.Render("Auto-rebind disabled (no baseline environment set). Set one via Edit customer to translate references by name."))
	}

	mappings, _ := customer.DeployMappings(alias)
	if len(mappings) == 0 {
		mappings, err = setupDeployMappings(all, workspaces)
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

	groups, err := buildDeployGroups(client, token, mappings, all, workspaces, rebinder, excluded)
	if err != nil {
		return err
	}

	filterIgnoredUnresolved(groups, customer)

	// Always compare first, then gate on an explicit "continue to deployment".
	fmt.Println(infoStyle.Render(fmt.Sprintf("Comparing %s → %s", env, targetsSummary(groups))))
	printGroupedCompare(groups)
	printRebindSummary(groups)
	printReportBindings(groups)
	printUnresolved(groups)

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

	useBulk, err := ui.Confirm("Use the bulk-import backend? (PREVIEW — default is the stable per-item backend)")
	if err != nil {
		return err
	}
	if useBulk {
		fmt.Println(warningStyle.Render("Bulk-import is a PREVIEW backend on a Fabric beta API — verify the deployed items afterwards."))
	}

	results, err := runDeploy(client, token, groups, rebinder, pickGroupedRows, ui.Confirm, useBulk)
	printDeployResults(results)
	if err != nil {
		return err
	}
	saveDeployHistory(customer, groups, results)
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
func buildDeployGroups(client APIClient, token string, mappings []config.DeployMapping, all []deploy.LocalItem, workspaces []fabric.Workspace, rb *deploy.Rebinder, excluded map[string]bool) ([]deployGroup, error) {
	groups := make([]deployGroup, 0, len(mappings))
	for _, m := range mappings {
		target, err := resolveWorkspaceByName(workspaces, m.Workspace)
		if err != nil {
			return nil, fmt.Errorf("mapping %q→%q: %w", m.Folder, m.Workspace, err)
		}

		items := filterExcludedTypes(deploy.ItemsInFolder(all, m.Folder), excluded)
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
		}
		g.Unresolved, g.Changes, g.Diffs = diffExistingRows(client, token, target, rows, rb)
		if rb != nil {
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
					subbed, _ := rb.ApplyCustomSubstitutions(it, part.Path, part.Content)
					_, outcome := rb.RebindReportConnection(it, subbed)
					g.ReportBindings = append(g.ReportBindings, outcome.ReportBindings...)
					g.Changes = append(g.Changes, outcome.Changes...)
					g.Unresolved = append(g.Unresolved, outcome.Unresolved...)
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
func diffExistingRows(client deploy.FabricClient, token string, target fabric.Workspace, rows []deploy.CompareRow, rb *deploy.Rebinder) ([]deploy.UnresolvedRef, []deploy.RebindChange, []ItemDiff) {
	var existsIdx []int
	for i := range rows {
		if rows[i].Class == deploy.ClassExists {
			existsIdx = append(existsIdx, i)
		}
	}
	if len(existsIdx) == 0 {
		return nil, nil, nil
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
	var itemDiffs []ItemDiff
	for j, idx := range existsIdx {
		c := compared[j]
		if results[j].err != nil {
			unverified++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rows[idx].Name(), results[j].err)
			}
			continue
		}
		if rows[idx].ItemType() != "Report" {
			// Binding outcomes (ReportBindings / Unresolved / Changes) for Report
			// items are owned exclusively by the dedicated pass in buildDeployGroups,
			// which runs ApplyCustomSubstitutions before RebindReportConnection so the
			// previewed binding matches what SubstituteParts publishes. Report content
			// IS still rewritten above so the content diff remains accurate; only the
			// binding outcome collection is intentionally skipped here to avoid
			// double-counting across the two passes.
			unresolved = append(unresolved, c.unresolved...)
			changes = append(changes, c.changes...)
		}
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
func setupDeployMappings(all []deploy.LocalItem, workspaces []fabric.Workspace) ([]config.DeployMapping, error) {
	folders := deploy.TopLevelFolders(all)
	if len(folders) == 0 {
		return nil, fmt.Errorf("couldn't detect any folders to map in the repo — add mappings via Edit customer instead")
	}
	const skipValue = "\x00skip"
	fmt.Println(infoStyle.Render("Set up which repo folder deploys to which workspace:"))
	var mappings []config.DeployMapping
	for _, folder := range folders {
		opts := []ui.FilterOption{{Label: "⋯ Skip this folder", Value: skipValue}}
		for _, w := range workspaces {
			opts = append(opts, ui.FilterOption{Label: w.DisplayName, Value: w.DisplayName})
		}
		chosen, err := ui.FilterMenu(fmt.Sprintf("Deploy %s/ to which workspace?", folder), opts, ui.DefaultFilterRowRenderer)
		if err != nil {
			return nil, err
		}
		if chosen == skipValue {
			continue
		}
		mappings = append(mappings, config.DeployMapping{Folder: folder, Workspace: chosen})
		fmt.Printf("  %s/ → %s\n", folder, chosen)
	}
	return mappings, nil
}

// deleteCount returns the total number of items to be deleted across all groups.
func deleteCount(m map[int][]deploy.DeleteTarget) int {
	n := 0
	for _, d := range m {
		n += len(d)
	}
	return n
}

// runDeploy lets the user cherry-pick across groups, confirms, and executes each
// group against its own workspace. Returns the aggregated per-item results. On a
// mid-run Execute failure it returns the results accumulated so far alongside the
// error, so callers should print results before checking err.
func runDeploy(
	client deploy.FabricClient,
	token string,
	groups []deployGroup,
	rb *deploy.Rebinder,
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
			bulkResults, berr := runBulkPublish(client, token, groups, selected, rb)
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
				results, groupPending, execErr := deploy.Execute(client, token, g.Target, plan, rb, modelsByWS, &done)
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
			if outcomes := deploy.RebindReports(client, token, modelsByWS, pending, rb != nil); len(outcomes) > 0 {
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
func runBulkPublish(client deploy.FabricClient, token string, groups []deployGroup, selected map[int][]deploy.LocalItem, rb *deploy.Rebinder) ([]deploy.Result, error) {
	type bucket struct {
		target fabric.Workspace
		items  []deploy.LocalItem
	}
	buckets := map[string]*bucket{}
	var order []string
	for i, g := range groups {
		items := selected[i]
		if len(items) == 0 {
			continue
		}
		b := buckets[g.Target.ID]
		if b == nil {
			b = &bucket{target: g.Target}
			buckets[g.Target.ID] = b
			order = append(order, g.Target.ID)
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
		r, err := deploy.BulkImport(client, token, b.target, b.items, rb)
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
// empty reports. When items WERE deployed but no folder/repo is configured it
// prints a skip notice. The report's diff section reflects every compared item
// while the results section reflects what was actually deployed (a cherry-picked
// subset shows fewer results than diffs). A write failure is non-fatal — the
// deploy already happened.
func saveDeployHistory(customer config.Customer, groups []deployGroup, results []deploy.Result) {
	if len(results) == 0 {
		return // nothing was published — no report to write
	}
	if customer.DeployHistoryPath == "" || customer.RepoPath == "" {
		fmt.Println(infoStyle.Render("No deploy-history folder set — skipping report. Set one with `futils edit`."))
		return
	}
	dir := filepath.Join(customer.RepoPath, customer.DeployHistoryPath)
	htmlDoc := renderDeployReport(groups, results)
	path, err := writeHistoryReport(dir, time.Now(), htmlDoc)
	if err != nil {
		fmt.Println(warningStyle.Render("Couldn't save deploy report: " + err.Error()))
		return
	}
	fmt.Println(infoStyle.Render("Saved deploy report: " + path))
}

// pickEnvironment shows the customer's environment aliases as a numbered menu.
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
	for _, c := range ordered {
		fmt.Printf("  %-12s %-24s %s → %s\n", c.Kind, c.Name, c.Old, c.New)
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
// enough context for the user to register an override (or ignore/strip them).
// Silent when everything resolved.
func printUnresolved(groups []deployGroup) {
	var total int
	for _, g := range groups {
		total += len(g.Unresolved)
	}
	if total == 0 {
		return
	}
	fmt.Println()
	fmt.Println(warningStyle.Render(fmt.Sprintf("%d unresolved reference(s) — left as-is. Register an override (Edit customer) to map them by name:", total)))
	for _, g := range groups {
		for _, u := range g.Unresolved {
			fmt.Printf("  %s in %s — looks like a %s (%s)\n", shortGUID(u.GUID), u.ItemName, u.ItemType, u.Location)
		}
	}
	fmt.Println()
}

// pickDeployScope lets the user deploy a single folder→workspace mapping or all
// of them. With one mapping it's a no-op. Returns the chosen subset.
func pickDeployScope(mappings []config.DeployMapping) ([]config.DeployMapping, error) {
	if len(mappings) <= 1 {
		return mappings, nil
	}
	opts := []ui.MenuOption{{Label: fmt.Sprintf("All (%d mappings)", len(mappings)), Value: "__all"}}
	for i, m := range mappings {
		opts = append(opts, ui.MenuOption{Label: fmt.Sprintf("%s/ → %s", m.Folder, m.Workspace), Value: fmt.Sprintf("%d", i)})
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
		fmt.Printf("\n%s in %s — looks like a %s (%s)\n", shortGUID(ref.GUID), ref.ItemName, ref.ItemType, ref.Location)
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
