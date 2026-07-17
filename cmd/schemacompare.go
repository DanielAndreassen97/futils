package cmd

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/schemacompare"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

// schemaCompareConcurrency caps total in-flight OneLake calls for a compare.
// The source and target sides fetch concurrently through ONE shared budget of
// this size, so it sets the real pipe width (the old per-side pool of 16 ran
// the two sides sequentially, peaking at 16). 32 roughly doubles GetTable
// throughput; the client's 429 backoff absorbs the extra load if OneLake
// pushes back. Tune here if a live run shows throttling.
const schemaCompareConcurrency = 32

// browseAllWorkspaces is the sentinel menu value for the "list every workspace"
// escape hatch in the source/target picker.
const browseAllWorkspaces = "\x00browse-all"

// newOneLakeAPI acquires the OneLake Table API client for a customer (a
// storage-scope token, separate from the Fabric one). A package var so demo
// mode can swap in the offline fake.
var newOneLakeAPI = func(customerName string) (schemacompare.OneLakeTableAPI, error) {
	storageToken, err := fabric.GetStorageToken(customerName)
	if err != nil {
		return nil, fmt.Errorf("acquire OneLake token: %w", err)
	}
	return schemacompare.NewClient(storageToken), nil
}

// intersectLakehousesByName returns the sorted display names that appear as a
// Lakehouse in both workspaces — the paired set we can compare.
func intersectLakehousesByName(src, tgt []fabric.Item) []string {
	inTgt := map[string]bool{}
	for _, it := range tgt {
		inTgt[it.DisplayName] = true
	}
	var names []string
	seen := map[string]bool{}
	for _, it := range src {
		if inTgt[it.DisplayName] && !seen[it.DisplayName] {
			names = append(names, it.DisplayName)
			seen[it.DisplayName] = true
		}
	}
	sort.Strings(names)
	return names
}

// filterToPresent keeps the chosen names that exist in present, preserving the
// chosen order — each side is fetched only for schemas it actually has.
func filterToPresent(chosen, present []string) []string {
	has := map[string]bool{}
	for _, s := range present {
		has[s] = true
	}
	var out []string
	for _, s := range chosen {
		if has[s] {
			out = append(out, s)
		}
	}
	return out
}

// unionSorted returns the sorted union of two name lists. Used for the schema
// pick-list so a schema present on only ONE side (e.g. destination-only) still
// enters the comparison instead of being silently ignored.
func unionSorted(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// pickSchemaCompareWorkspace lets the user choose a workspace for one side of
// a schema compare: first the customer's environment aliases (default), or
// "Browse all workspaces…" to pick from every accessible workspace. Returns
// the chosen workspace's display name and ID.
func pickSchemaCompareWorkspace(side string, customer config.Customer, workspaces []fabric.Workspace) (string, string, error) {
	wsByName := map[string]string{}
	for _, w := range workspaces {
		wsByName[w.DisplayName] = w.ID
	}

	var opts []ui.MenuOption
	for _, e := range customer.Environments {
		opts = append(opts, ui.MenuOption{Label: e.Alias, Value: "env:" + e.Alias})
	}
	opts = append(opts, ui.MenuOption{Label: "Browse all workspaces…", Value: browseAllWorkspaces})

	choice, err := ui.NumberMenu(fmt.Sprintf("Choose %s", side), opts)
	if err != nil {
		return "", "", err
	}

	var wsName string
	if choice == browseAllWorkspaces {
		names := make([]string, 0, len(workspaces))
		for _, w := range workspaces {
			names = append(names, w.DisplayName)
		}
		sort.Strings(names)
		wsName, err = ui.NumberMenu(fmt.Sprintf("%s workspace", side), ui.MenuOptionsFromStrings(names))
		if err != nil {
			return "", "", err
		}
	} else {
		alias := choice[len("env:"):]
		env, _ := customer.EnvironmentByAlias(alias)
		switch len(env.Workspaces) {
		case 0:
			return "", "", fmt.Errorf("environment %q has no workspaces", alias)
		case 1:
			wsName = env.Workspaces[0]
		default:
			wsName, err = ui.NumberMenu(fmt.Sprintf("%s workspace (%s)", side, alias),
				ui.MenuOptionsFromStrings(env.Workspaces))
			if err != nil {
				return "", "", err
			}
		}
	}

	id, ok := wsByName[wsName]
	if !ok {
		return "", "", fmt.Errorf("workspace %q not found or not accessible", wsName)
	}
	return wsName, id, nil
}

// SchemaCompare runs the interactive schema-compare flow.
func SchemaCompare(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	customerName, customer, err := selectCustomer(cfg)
	if err != nil {
		return err
	}

	token, err := DefaultAPI.GetAccessToken(customerName)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	workspaces, err := DefaultAPI.ListWorkspaces(token)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	srcName, srcID, err := pickSchemaCompareWorkspace("source", customer, workspaces)
	if err != nil {
		return err
	}
	tgtName, tgtID, err := pickSchemaCompareWorkspace("destination", customer, workspaces)
	if err != nil {
		return err
	}

	srcLakes, err := DefaultAPI.ListItemsByType(token, srcID, "Lakehouse")
	if err != nil {
		return fmt.Errorf("list source lakehouses: %w", err)
	}
	tgtLakes, err := DefaultAPI.ListItemsByType(token, tgtID, "Lakehouse")
	if err != nil {
		return fmt.Errorf("list destination lakehouses: %w", err)
	}
	common := intersectLakehousesByName(srcLakes, tgtLakes)
	if len(common) == 0 {
		fmt.Println(infoStyle.Render("No lakehouses with matching names in both workspaces."))
		return nil
	}
	chosenLakes, err := ui.MultiSelect("Lakehouses to compare", common, common)
	if err != nil {
		return err
	}
	if len(chosenLakes) == 0 {
		return nil
	}

	api, err := newOneLakeAPI(customerName)
	if err != nil {
		return err
	}

	idByName := func(items []fabric.Item, name string) string {
		for _, it := range items {
			if it.DisplayName == name {
				return it.ID
			}
		}
		return ""
	}

	var diffs []schemacompare.LakehouseDiff
	for _, lhName := range chosenLakes {
		srcLhID := idByName(srcLakes, lhName)
		tgtLhID := idByName(tgtLakes, lhName)

		// List BOTH sides and take the union: a destination-only schema must be
		// compared too (its tables would otherwise silently pass as "identical").
		srcSchemas, err := api.ListSchemas(srcID, srcLhID)
		if err != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: list source schemas failed: %v", lhName, err)))
			continue
		}
		tgtSchemas, err := api.ListSchemas(tgtID, tgtLhID)
		if err != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: list destination schemas failed: %v", lhName, err)))
			continue
		}
		schemas := unionSorted(srcSchemas, tgtSchemas)
		if len(schemas) == 0 {
			fmt.Println(infoStyle.Render(fmt.Sprintf("%s: no schemas.", lhName)))
			continue
		}
		chosenSchemas, err := ui.MultiSelect(fmt.Sprintf("Schemas in %s", lhName), schemas, schemas)
		if err != nil {
			return err
		}
		if len(chosenSchemas) == 0 {
			continue
		}

		// Fetch each side only for the schemas it actually has — asking the API
		// for a schema missing on that side 404s the whole fetch. Tables under a
		// one-sided schema surface in Compare as missing on the other side.
		srcPick := filterToPresent(chosenSchemas, srcSchemas)
		tgtPick := filterToPresent(chosenSchemas, tgtSchemas)

		sp := ui.NewSpinner(fmt.Sprintf("Comparing %s…", lhName))
		sp.Start()
		// Fetch both sides concurrently through one shared budget: the two
		// halves are independent, so overlapping them fills the same pipe
		// instead of waiting for source to finish before target starts.
		fetcher := schemacompare.NewFetcher(api, schemaCompareConcurrency)
		var (
			srcSchema, tgtSchema map[string]schemacompare.TableSchema
			srcErr, tgtErr       error
			fwg                  sync.WaitGroup
		)
		fwg.Add(2)
		go func() { defer fwg.Done(); srcSchema, srcErr = fetcher.Fetch(srcID, srcLhID, srcPick) }()
		go func() { defer fwg.Done(); tgtSchema, tgtErr = fetcher.Fetch(tgtID, tgtLhID, tgtPick) }()
		fwg.Wait()
		sp.Stop()
		if srcErr != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: fetch source failed: %v", lhName, srcErr)))
			continue
		}
		if tgtErr != nil {
			fmt.Println(warningStyle.Render(fmt.Sprintf("%s: fetch destination failed: %v", lhName, tgtErr)))
			continue
		}
		tables, matching := schemacompare.Compare(srcSchema, tgtSchema)
		diffs = append(diffs, schemacompare.LakehouseDiff{
			Lakehouse: lhName, Schemas: chosenSchemas, Tables: tables, Matching: matching,
		})
	}

	fmt.Print(renderSchemaCompareTerminal(srcName, tgtName, diffs))

	if ok, cerr := ui.Confirm("Open report in browser?"); cerr == nil && ok {
		if err := showSchemaCompareInBrowser(srcName, tgtName, diffs); err != nil {
			fmt.Println(warningStyle.Render("Couldn't open report: " + err.Error()))
		}
	}
	return nil
}

// showSchemaCompareInBrowser writes the HTML report to a temp file and opens
// it. The file is deliberately NOT deleted afterwards — a deletion timer races
// the browser's load (see showDiffsInBrowser); the OS cleans its temp dir.
func showSchemaCompareInBrowser(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) error {
	doc := renderSchemaCompareReport(srcLabel, tgtLabel, diffs)
	f, err := os.CreateTemp("", "futils-schema-compare-*.html")
	if err != nil {
		return err
	}
	path := f.Name()
	if _, err := f.WriteString(doc); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return openInBrowser(path)
}
