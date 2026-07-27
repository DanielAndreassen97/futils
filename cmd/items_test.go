package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
)

func itemFixture() []fabric.Item {
	return []fabric.Item{
		{ID: "r1", DisplayName: "Sales overview", Type: "Report"},
		{ID: "n2", DisplayName: "nb_zulu", Type: "Notebook"},
		{ID: "l1", DisplayName: "lh_bronze", Type: "Lakehouse"},
		{ID: "n1", DisplayName: "nb_alpha", Type: "Notebook"},
		{ID: "p1", DisplayName: "PL_daily", Type: "DataPipeline"},
		{ID: "x1", DisplayName: "some_reflex", Type: "Reflex"},
		{ID: "s1", DisplayName: "Sales model", Type: "SemanticModel"},
	}
}

func headerLabels(opts []ui.FilterOption) []string {
	var out []string
	for _, o := range opts {
		if o.IsHeader {
			out = append(out, o.Label)
		}
	}
	return out
}

func TestGroupItemsByTypeOrdersByUsefulness(t *testing.T) {
	// Not alphabetical: the types people act on most come first, and anything
	// futils has no opinion about falls to the end in a stable order.
	got := headerLabels(groupItemsByType(itemFixture()))

	want := []string{
		"NOTEBOOK · 2", "DATAPIPELINE · 1", "SEMANTICMODEL · 1",
		"REPORT · 1", "LAKEHOUSE · 1", "REFLEX · 1",
	}
	if len(got) != len(want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("headers = %v, want %v", got, want)
		}
	}
}

func TestGroupItemsByTypeSortsInsideAGroup(t *testing.T) {
	opts := groupItemsByType(itemFixture())

	var notebooks []string
	inNotebooks := false
	for _, o := range opts {
		if o.IsHeader {
			inNotebooks = strings.HasPrefix(o.Label, "NOTEBOOK")
			continue
		}
		if inNotebooks {
			notebooks = append(notebooks, o.Label)
		}
	}
	if len(notebooks) != 2 || notebooks[0] != "nb_alpha" || notebooks[1] != "nb_zulu" {
		t.Errorf("notebooks = %v, want alphabetical", notebooks)
	}
}

func TestGroupItemsByTypeMetaHoldsTheItem(t *testing.T) {
	// The action menu needs the item's type and ID, so the row has to carry the
	// whole item rather than just its name.
	for _, o := range groupItemsByType(itemFixture()) {
		if o.IsHeader {
			continue
		}
		it, ok := o.Meta.(fabric.Item)
		if !ok {
			t.Fatalf("row %q meta is %T, want a fabric.Item", o.Label, o.Meta)
		}
		if it.DisplayName != o.Label || o.Value != it.ID {
			t.Errorf("row %q carries %+v", o.Label, it)
		}
	}
}

func TestGroupItemsByTypeEmpty(t *testing.T) {
	if got := groupItemsByType(nil); len(got) != 0 {
		t.Errorf("got %d rows for no items, want none", len(got))
	}
}

func TestItemTypeColorMatchesTheFabricIconSet(t *testing.T) {
	// Taken from Microsoft's own Fabric icon set, not invented: a lakehouse is
	// blue and a notebook is green in the portal, so they are here too.
	cases := map[string]string{
		"Notebook":      "#45913e",
		"DataPipeline":  "#45913e",
		"Lakehouse":     "#2661be",
		"Warehouse":     "#20b6ef",
		"SemanticModel": "#744fb5",
		"Report":        "#bc7d00",
	}
	for typ, want := range cases {
		bg, _ := itemTypeColor(typ)
		if string(bg) != want {
			t.Errorf("%s background = %q, want %q", typ, bg, want)
		}
	}
	// An unknown type must fall back rather than render unstyled.
	bg, fg := itemTypeColor("SomethingMicrosoftShipsNextYear")
	if bg == "" || fg == "" {
		t.Error("an unknown item type must still get a colour")
	}
}

func TestItemActionsOffersRunOnlyForRunnableTypes(t *testing.T) {
	runnable := map[string]string{
		"Notebook":      "Run notebook",
		"DataPipeline":  "Run pipeline",
		"SemanticModel": "Refresh tables",
	}
	for typ, label := range runnable {
		if got := actionLabel(itemActions(fabric.Item{Type: typ}), itemActionRun); got != label {
			t.Errorf("%s run label = %q, want %q", typ, got, label)
		}
		if badge := actionBadge(itemActions(fabric.Item{Type: typ}), itemActionRun); badge != "" {
			t.Errorf("%s run is badged %q but should be available", typ, badge)
		}
	}
	// A lakehouse cannot be run. The entry stays, so the menu does not reshuffle
	// between items, but it is badged and refused.
	if badge := actionBadge(itemActions(fabric.Item{Type: "Lakehouse"}), itemActionRun); badge == "" {
		t.Error("a lakehouse's run entry must be badged unavailable")
	}
}

func TestItemActionsOffersMoveOnlyForSupportedTypes(t *testing.T) {
	for typ := range moveSupportedTypes {
		if badge := actionBadge(itemActions(fabric.Item{Type: typ}), itemActionMove); badge != "" {
			t.Errorf("%s move is badged %q but move supports it", typ, badge)
		}
	}
	for _, typ := range []string{"Lakehouse", "DataPipeline", "Warehouse"} {
		if badge := actionBadge(itemActions(fabric.Item{Type: typ}), itemActionMove); badge == "" {
			t.Errorf("%s move must be badged unavailable", typ)
		}
	}
}

func TestItemActionsAlwaysOffersMetadataAndBack(t *testing.T) {
	// Rename, description and delete work for every type, so they are never
	// badged; Back must always exist or esc is the only way out.
	for _, typ := range []string{"Notebook", "Lakehouse", "Reflex"} {
		opts := itemActions(fabric.Item{Type: typ})
		for _, v := range []string{itemActionRename, itemActionDesc, itemActionDelete, itemActionBack} {
			if actionLabel(opts, v) == "" {
				t.Errorf("%s is missing action %q", typ, v)
			}
		}
		if badge := actionBadge(opts, itemActionRename); badge != "" {
			t.Errorf("%s rename is badged %q", typ, badge)
		}
	}
}

func actionLabel(opts []ui.MenuOption, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Label
		}
	}
	return ""
}

func actionBadge(opts []ui.MenuOption, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Badge
		}
	}
	return ""
}

func TestValidateItemName(t *testing.T) {
	siblings := []fabric.Item{
		{ID: "a", DisplayName: "nb_alpha"},
		{ID: "b", DisplayName: "nb_beta"},
	}
	cases := []struct{ name, input, current, want string }{
		{"empty", "", "", "empty"},
		{"whitespace", "   ", "", "empty"},
		{"taken by a sibling", "nb_beta", "nb_alpha", "already"},
		{"unchanged", "nb_alpha", "nb_alpha", "differ"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateItemName(c.input, siblings, c.current)
			if err == nil {
				t.Fatalf("validateItemName(%q) = nil, want an error", c.input)
			}
			if !strings.Contains(strings.ToLower(err.Error()), c.want) {
				t.Errorf("error %q must mention %q", err, c.want)
			}
		})
	}
	if err := validateItemName("nb_gamma", siblings, "nb_alpha"); err != nil {
		t.Errorf("a fresh name must be accepted, got %v", err)
	}
}

func TestValidateItemDescriptionUsesTheItemLimit(t *testing.T) {
	// 256, not the 4000 a workspace allows.
	if err := validateItemDescription(strings.Repeat("x", fabric.MaxItemDescLen)); err != nil {
		t.Errorf("a description at the limit must be allowed, got %v", err)
	}
	err := validateItemDescription(strings.Repeat("x", fabric.MaxItemDescLen+1))
	if err == nil || !strings.Contains(err.Error(), "256") {
		t.Errorf("over-long description error = %v, want a mention of 256", err)
	}
	if err := validateItemDescription(""); err != nil {
		t.Errorf("an empty description must be allowed, got %v", err)
	}
}

func TestRenderItemRefsNamesEachKind(t *testing.T) {
	refs := []config.ItemRef{
		{Customer: "Fabrikam", Kind: config.RefFavorite},
		{Customer: "Fabrikam", Kind: config.RefPostDeployRun},
		{Customer: "Contoso", Kind: config.RefSubstitutionTarget, Detail: "old-guid"},
	}
	got := renderItemRefs(refs)
	for _, want := range []string{"Fabrikam", "Contoso", "favourite", "post-deploy run", "substitution target", "old-guid"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered refs missing %q:\n%s", want, got)
		}
	}
	if renderItemRefs(nil) != "" {
		t.Error("no references must render as the empty string")
	}
}

// wsItemAPI extends the workspace fake with an item map and records the item
// mutations, so a flow test can assert both the API call and the config write.
func wsItemAPI() *wsFakeAPI {
	api := wsTestAPI()
	api.items["ws-fin"] = []fabric.Item{
		{ID: "nb1", DisplayName: "nb_ingest", Type: "Notebook", Description: "Loads data"},
		{ID: "lh1", DisplayName: "lh_bronze", Type: "Lakehouse"},
	}
	return api
}

// itemConfigFile writes a config whose customer names "nb_ingest" as both a
// favourite and a post-deploy run.
func itemConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Customers: map[string]config.Customer{
		"Fabrikam": {
			Environments:   []config.Environment{{Alias: "DEV", Workspaces: []string{"DW - Finance"}}},
			Favorites:      []config.NotebookFavorite{{Name: "nb_ingest"}},
			PostDeployRuns: []string{"nb_ingest"},
		},
	}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestItemRenameLeavesConfigAloneWhenDeclined(t *testing.T) {
	// The whole point of not auto-repairing: the config entries may still be
	// correct for other environments, so declining must change nothing.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionRename},
		inputs:      []string{"nb_ingest_customers"},
		// confirm 1: perform the rename. confirm 2: update config — declined.
		confirms: []bool{true, false},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.renamedItems) != 1 || api.renamedItems[0] != [2]string{"nb1", "nb_ingest_customers"} {
		t.Fatalf("renamedItems = %v", api.renamedItems)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindItemRefs(cfg, "nb_ingest"); len(refs) != 2 {
		t.Errorf("config changed despite declining: %+v", refs)
	}
	if !strings.Contains(out, "environment-agnostic") {
		t.Errorf("the user must be told why futils will not guess:\n%s", out)
	}
}

func TestItemRenameUpdatesConfigWhenAccepted(t *testing.T) {
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionRename},
		inputs:      []string{"nb_ingest_customers"},
		confirms:    []bool{true, true},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	cfg, _ := config.Load(path)
	if refs := config.FindItemRefs(cfg, "nb_ingest_customers"); len(refs) != 2 {
		t.Errorf("new name has %d config references, want 2", len(refs))
	}
}

func TestItemRenameAPIFailureLeavesConfigUntouched(t *testing.T) {
	path := itemConfigFile(t)
	api := wsItemAPI()
	api.renameItemErr = errors.New("update item 403: needs read and write permission on the item")
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionRename},
		inputs:      []string{"nb_ingest_customers"},
		confirms:    []bool{true},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err == nil {
			t.Fatal("expected the API error to surface")
		}
	})

	cfg, _ := config.Load(path)
	if refs := config.FindItemRefs(cfg, "nb_ingest"); len(refs) != 2 {
		t.Errorf("config changed despite the rename failing: %+v", refs)
	}
}

func TestItemDeleteOfADataBearingTypeNeedsTheTypedWord(t *testing.T) {
	// A lakehouse takes its tables and files with it, so it gets the same
	// confirmation a workspace delete gets.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "lh1"},
		numberPicks: []string{itemActionDelete},
		typedOK:     []bool{false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.deletedItems) != 0 {
		t.Errorf("delete ran without the typed word: %v", api.deletedItems)
	}
	if len(h.typedWords) != 1 || h.typedWords[0] != "Yes" {
		t.Errorf("typed words = %v, want one Yes prompt", h.typedWords)
	}
}

func TestItemDeleteOfANotebookUsesAPlainConfirm(t *testing.T) {
	// A notebook's content lives in git; the typed word is reserved for deletes
	// that destroy data futils cannot get back.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionDelete},
		// confirm 1: delete. confirm 2: remove config entries — declined.
		confirms: []bool{true, false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.deletedItems) != 1 || api.deletedItems[0] != "nb1" {
		t.Fatalf("deletedItems = %v", api.deletedItems)
	}
	if len(h.typedPrompts) != 0 {
		t.Errorf("a notebook delete must not demand a typed word, got %v", h.typedPrompts)
	}
	cfg, _ := config.Load(path)
	if refs := config.FindItemRefs(cfg, "nb_ingest"); len(refs) != 2 {
		t.Errorf("config changed despite declining: %+v", refs)
	}
}

func TestItemDescriptionUpdate(t *testing.T) {
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionDesc},
		inputs:      []string{"Loads customers into bronze"},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.itemDescSet) != 1 || api.itemDescSet[0] != [2]string{"nb1", "Loads customers into bronze"} {
		t.Errorf("itemDescSet = %v", api.itemDescSet)
	}
}

func TestItemRunRefusesANonRunnableType(t *testing.T) {
	// The action is badged in the menu, but a stale selection must still be
	// refused before any request goes out.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "lh1"},
		numberPicks: []string{itemActionRun},
	}
	h.install(t)

	out := captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if !strings.Contains(out, "nothing to run") {
		t.Errorf("output must explain why a lakehouse cannot run:\n%s", out)
	}
}

func TestWorkspaceScreenPinsItsActionsAboveTheItems(t *testing.T) {
	// The actions have to be pinned, or typing a filter would hide Rename.
	opts := workspaceScreenOptions([]fabric.Item{
		{ID: "n1", DisplayName: "nb_alpha", Type: "Notebook"},
	}, true)

	pinned := 0
	for _, o := range opts {
		if o.Pinned {
			pinned++
		}
	}
	if pinned != 4 {
		t.Errorf("%d pinned rows, want the four workspace actions", pinned)
	}
	for i := 0; i < 4; i++ {
		if !opts[i].Pinned {
			t.Fatalf("row %d (%q) is not pinned — actions must come first", i, opts[i].Label)
		}
	}
	if opts[len(opts)-1].Label != "nb_alpha" {
		t.Errorf("last row = %q, want the item", opts[len(opts)-1].Label)
	}
}

func TestWorkspaceScreenBadgesActionsForANonAdmin(t *testing.T) {
	opts := workspaceScreenOptions(nil, false)
	for _, o := range opts[:3] {
		if o.Badge == "" {
			t.Errorf("action %q is not badged for a non-admin", o.Label)
		}
	}
	if opts[3].Badge != "" {
		t.Errorf("Back must never be badged, got %q", opts[3].Badge)
	}
}

func TestBackFromAnItemReturnsToTheWorkspaceNotTheList(t *testing.T) {
	// Backing out of an item is a step up, not a step out. Landing on the
	// tenant-wide workspace list loses the place you were working in — and
	// costs a full reload to get back to it.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		// Workspace, then the item, then the workspace screen again (which runs
		// out of picks and backs out for real).
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionBack},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	// Two workspace-screen renders: the first visit, and the one Back returns
	// to. Three would mean it bounced out to the list and back in.
	if api.listWorkspacesCalls != 1 {
		t.Errorf("ListWorkspaces called %d times, want 1 — Back must not reload the tenant", api.listWorkspacesCalls)
	}
}

func TestBackFromAnItemDoesNotRelistItems(t *testing.T) {
	// Nothing changed, so the cached item list is still correct.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionBack},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if api.listItemsCalls != 1 {
		t.Errorf("ListItems called %d times, want 1 — backing out changes nothing", api.listItemsCalls)
	}
}

func TestACancelledItemRenameDoesNotRelistItems(t *testing.T) {
	// Declining the rename leaves the workspace exactly as it was, so the
	// cached list is still good.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionRename},
		inputs:      []string{"nb_new_name"},
		confirms:    []bool{false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.renamedItems) != 0 {
		t.Fatalf("a declined rename hit the API: %v", api.renamedItems)
	}
	if api.listItemsCalls != 1 {
		t.Errorf("ListItems called %d times, want 1 after a cancelled rename", api.listItemsCalls)
	}
}

func TestACompletedItemRenameRelistsItemsOnce(t *testing.T) {
	// The rename DID land, so the cached names are stale and the screen must
	// re-list before drawing again — exactly once.
	path := itemConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "nb1"},
		numberPicks: []string{itemActionRename},
		inputs:      []string{"nb_ingest_customers"},
		confirms:    []bool{true, false},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if len(api.renamedItems) != 1 {
		t.Fatalf("renamedItems = %v", api.renamedItems)
	}
	if api.listItemsCalls != 2 {
		t.Errorf("ListItems called %d times, want 2 — one per screen render", api.listItemsCalls)
	}
	// The tenant list is untouched by an item rename.
	if api.listWorkspacesCalls != 1 {
		t.Errorf("ListWorkspaces called %d times, want 1", api.listWorkspacesCalls)
	}
}

func TestAWorkspaceRenameReloadsTheTenantList(t *testing.T) {
	// A workspace rename changes the list you came from, so that one DOES have
	// to be refetched.
	path := wsConfigFile(t)
	api := wsItemAPI()
	h := &wsHarness{
		filterPicks: []string{"ws-fin", wsActionRename},
		inputs:      []string{"DW - Finance PROD"},
		confirms:    []bool{true},
	}
	h.install(t)

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if api.listWorkspacesCalls < 2 {
		t.Errorf("ListWorkspaces called %d times, want a reload after the rename", api.listWorkspacesCalls)
	}
}

func TestMoveReusesTheCachedWorkspaceList(t *testing.T) {
	// The destination picker needs the whole tenant, which the screen the move
	// was launched from already loaded. Fetching it again is a second identical
	// request in the same breath.
	path := itemConfigFile(t)
	api := wsItemAPI()
	api.items["ws-fin"] = []fabric.Item{
		{ID: "r1", DisplayName: "Sales overview", Type: "Report"},
	}
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "r1"},
		numberPicks: []string{itemActionMove},
	}
	h.install(t)

	// The move flow has its own picker hook; back out of it immediately so the
	// test measures the listing, not the whole move.
	origMoveFilter := moveFilterPicker
	t.Cleanup(func() { moveFilterPicker = origMoveFilter })
	moveFilterPicker = func(string, []ui.FilterOption, ui.FilterRowRenderer) (string, error) {
		return "", ui.ErrGoBack
	}

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if api.listWorkspacesCalls != 1 {
		t.Errorf("ListWorkspaces called %d times, want 1 — move must reuse the loaded list", api.listWorkspacesCalls)
	}
}

func TestWorkspaceScreenPinsFourActionsInOrder(t *testing.T) {
	// A workspace with no items shows nothing but these rows, so they have to
	// behave as a numbered menu. The digits themselves are the widget's job —
	// FilterMenu numbers pinned rows by position and dispatches on the same
	// positions, so a label written "1)" here could drift from the key that
	// selects it.
	opts := workspaceScreenOptions(nil, true)

	want := []string{"Rename workspace", "Edit description", "Delete workspace", "Back"}
	if len(opts) != len(want) {
		t.Fatalf("got %d rows for an empty workspace, want just the four actions", len(opts))
	}
	for i := range want {
		if opts[i].Label != want[i] {
			t.Errorf("row %d = %q, want %q", i, opts[i].Label, want[i])
		}
		if !opts[i].Pinned {
			t.Errorf("row %d (%q) is not pinned", i, opts[i].Label)
		}
	}
}

func TestMoveDestinationPickerIsGroupedByRole(t *testing.T) {
	// Choosing a destination is choosing somewhere to write. A flat list of
	// forty names hides the one fact that decides whether the move works.
	idx := workspaceIndex{
		Workspaces: []fabric.Workspace{
			{ID: "a1", DisplayName: "Admin target", Type: "Workspace", CapacityID: "cap-1"},
			{ID: "v1", DisplayName: "Viewer target", Type: "Workspace"},
			{ID: "src", DisplayName: "Source", Type: "Workspace"},
		},
		RoleOf:     map[string]string{"a1": "Admin", "v1": "Viewer", "src": "Admin"},
		Capacities: []fabric.Capacity{{ID: "cap-1", DisplayName: "Prod", SKU: "F64", Region: "Norway East"}},
	}

	var seen []ui.FilterOption
	orig := moveFilterPicker
	t.Cleanup(func() { moveFilterPicker = orig })
	moveFilterPicker = func(_ string, opts []ui.FilterOption, _ ui.FilterRowRenderer) (string, error) {
		seen = opts
		return "a1", nil
	}

	got, err := pickWorkspace("Select destination workspace", idx, "src")
	if err != nil {
		t.Fatalf("pickWorkspace: %v", err)
	}
	if got.ID != "a1" {
		t.Errorf("picked %q, want a1", got.ID)
	}

	var headers, rows []string
	for _, o := range seen {
		if o.IsHeader {
			headers = append(headers, o.Label)
			continue
		}
		rows = append(rows, o.Label)
	}
	if len(headers) != 2 || headers[0] != "ADMIN · 1" || headers[1] != "VIEWER · 1" {
		t.Errorf("headers = %v, want an ADMIN and a VIEWER section", headers)
	}
	// The source must not be offered as its own destination.
	for _, r := range rows {
		if r == "Source" {
			t.Error("the source workspace was offered as a destination")
		}
	}
	// Rows carry the licence tier so the picker can colour it, same as the
	// workspace screen.
	for _, o := range seen {
		if o.IsHeader {
			continue
		}
		if _, ok := o.Meta.(workspaceTier); !ok {
			t.Errorf("row %q meta is %T, want a workspaceTier", o.Label, o.Meta)
		}
	}
}

func TestMoveFromTheBrowserStillReusesTheCachedIndex(t *testing.T) {
	// The grouped view needs roles and capacities as well as the list. All three
	// were already loaded by the screen the move was launched from.
	path := itemConfigFile(t)
	api := wsItemAPI()
	api.items["ws-fin"] = []fabric.Item{{ID: "r1", DisplayName: "Sales overview", Type: "Report"}}
	h := &wsHarness{
		filterPicks: []string{"ws-fin", "r1"},
		numberPicks: []string{itemActionMove},
	}
	h.install(t)

	origMoveFilter := moveFilterPicker
	t.Cleanup(func() { moveFilterPicker = origMoveFilter })
	moveFilterPicker = func(string, []ui.FilterOption, ui.FilterRowRenderer) (string, error) {
		return "", ui.ErrGoBack
	}

	captureStdout(t, func() {
		if err := WorkspacesWithAPI(path, api); err != nil {
			t.Fatalf("WorkspacesWithAPI: %v", err)
		}
	})

	if api.listWorkspacesCalls != 1 {
		t.Errorf("ListWorkspaces called %d times, want 1", api.listWorkspacesCalls)
	}
}
