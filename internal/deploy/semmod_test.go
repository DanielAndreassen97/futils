package deploy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// semmodOneLake builds a Direct Lake on OneLake expression referencing a
// workspace GUID + lakehouse GUID, the on-disk form confirmed in the design doc.
func semmodOneLake(wsGUID, lhGUID string) []byte {
	return []byte(`expression DatabaseQuery =
		let
			Source = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/` + wsGUID + `/` + lhGUID + `")
		in
			Source
`)
}

func TestRebindOneLakeSource(t *testing.T) {
	rb := newRebindFixture(t, nil)
	// devSilverLH lives in dev-data (workspace "dev-data"); target LH_Silver is
	// test-silver-lh in test-data.
	in := string(semmodOneLake("dev-data", devSilverLH))
	var out RebindOutcome
	got := rb.rebindOneLakeSources(in, &out)
	if !strings.Contains(got, "test-silver-lh") {
		t.Errorf("lakehouse GUID not rebound:\n%s", got)
	}
	if !strings.Contains(got, "test-data") || strings.Contains(got, devSilverLH) {
		t.Errorf("workspace GUID not rebound:\n%s", got)
	}
	// One Lakehouse change + one Workspace change.
	var lh, ws int
	for _, c := range out.Changes {
		switch c.Kind {
		case "Lakehouse":
			lh++
		case "Workspace":
			ws++
		}
	}
	if lh != 1 || ws != 1 {
		t.Fatalf("changes = %#v (want Lakehouse:1 Workspace:1)", out.Changes)
	}
}

// A Direct Lake source names its workspace in the URL, so an unresolved one
// carries that workspace and joins the deploy's per-workspace question. In a
// common workspace the same source resolves to itself and stays as-is.
func TestRebindOneLakeSourceCommonOrUnregisteredWorkspace(t *testing.T) {
	in := string(semmodOneLake("ingest", commonLH))

	var out RebindOutcome
	newRebindFixture(t, nil).rebindOneLakeSources(in, &out)
	if len(out.Unresolved) != 1 || out.Unresolved[0].WorkspaceID != "ingest" {
		t.Errorf("unresolved source must carry its workspace: %#v", out.Unresolved)
	}

	out = RebindOutcome{}
	got := withCommonIngest(t, newRebindFixture(t, nil)).rebindOneLakeSources(in, &out)
	if got != in || len(out.Unresolved) != 0 || len(out.Changes) != 0 {
		t.Errorf("a common source must stay as-is: unresolved=%#v changes=%#v", out.Unresolved, out.Changes)
	}
}

// TestRebindOneLakeSharedWorkspaceDifferentTargets proves that two OneLake
// sources sharing the SAME baseline workspace whose lakehouses resolve to
// DIFFERENT target workspaces are each rewritten to their own workspace — the
// old-value dedup must not funnel both URLs into the first target workspace.
func TestRebindOneLakeSharedWorkspaceDifferentTargets(t *testing.T) {
	const (
		lhOne = "44444444-4444-4444-4444-444444444444"
		lhTwo = "55555555-5555-5555-5555-555555555555"
		tgtA  = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		tgtB  = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-a", DisplayName: "TGT A"},
			{ID: "tgt-b", DisplayName: "TGT B"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-data": {
				{ID: lhOne, DisplayName: "LH_One", Type: "Lakehouse"},
				{ID: lhTwo, DisplayName: "LH_Two", Type: "Lakehouse"},
			},
			"tgt-a": {{ID: tgtA, DisplayName: "LH_One", Type: "Lakehouse"}},
			"tgt-b": {{ID: tgtB, DisplayName: "LH_Two", Type: "Lakehouse"}},
		},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1], f.workspaces[2]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let
	A = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/dev-data/` + lhOne + `"),
	B = AzureStorage.DataLake("https://onelake.dfs.fabric.microsoft.com/dev-data/` + lhTwo + `")
in
	B`
	got, outcome := rb.RebindSemanticModel([]byte(in), nil)
	s := string(got)
	if !strings.Contains(s, "onelake.dfs.fabric.microsoft.com/tgt-a/"+tgtA) {
		t.Errorf("LH_One must land in tgt-a:\n%s", s)
	}
	if !strings.Contains(s, "onelake.dfs.fabric.microsoft.com/tgt-b/"+tgtB) {
		t.Errorf("LH_Two must land in tgt-b (not funneled into tgt-a):\n%s", s)
	}
	if len(outcome.Unresolved) != 0 {
		t.Errorf("both lakehouses resolve — no unresolved expected, got %+v", outcome.Unresolved)
	}
}

// devConfigEP / testConfigEP / unknownConfigEP are the DEV/TEST/unresolvable
// baked SQL-endpoint GUIDs for newSemmodSQLRebinder. Production endpoint ids
// are always GUIDs, so the fixture uses obviously-fake ones rather than
// readable ids (a readable id would now be misread as the NAME-form branch).
const (
	devConfigEP     = "eeeeeeee-1111-1111-1111-111111111111"
	testConfigEP    = "eeeeeeee-2222-2222-2222-222222222222"
	unknownConfigEP = "eeeeeeee-0000-0000-0000-000000000000"
)

func newSemmodSQLRebinder(t *testing.T) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DW - DEV - Config"},
			{ID: "test-config", DisplayName: "DW - TEST - Config"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-config":  {{ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}, {ID: devConfigEP, DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}},
			"test-config": {{ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"dev-lh":  {"devhost.datawarehouse.fabric.microsoft.com", devConfigEP},
			"test-lh": {"testhost.datawarehouse.fabric.microsoft.com", testConfigEP},
		},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1]},
		nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

func semmodSQL(host, epID string) string {
	return `expression DatabaseQuery =
		let
			Source = Sql.Database("` + host + `", "` + epID + `")
		in
			Source
`
}

func TestRebindSQLSource(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	in := semmodSQL("devhost.datawarehouse.fabric.microsoft.com", devConfigEP)
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, "testhost.datawarehouse.fabric.microsoft.com") || !strings.Contains(got, testConfigEP) {
		t.Errorf("SQL endpoint not rebound:\n%s", got)
	}
	if strings.Contains(got, "devhost") || strings.Contains(got, devConfigEP) {
		t.Errorf("baseline endpoint still present:\n%s", got)
	}
	if len(out.Changes) != 2 { // host + id
		t.Fatalf("changes = %#v (want host + id)", out.Changes)
	}
	for _, c := range out.Changes {
		if c.Kind != "SQL endpoint" {
			t.Errorf("unexpected change kind %q", c.Kind)
		}
	}
}

func TestRebindSQLSourceUnresolved(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	in := semmodSQL("unknownhost.datawarehouse.fabric.microsoft.com", unknownConfigEP)
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, unknownConfigEP) {
		t.Error("unresolved endpoint should be left unchanged")
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].ItemType != "SQL endpoint" {
		t.Fatalf("unresolved = %#v", out.Unresolved)
	}
}

func TestRebindSemanticModelSQLVariant(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	// SQL variant only (this fixture has no data workspace for OneLake).
	in := []byte(semmodSQL("devhost.datawarehouse.fabric.microsoft.com", devConfigEP))
	out, outcome := rb.RebindSemanticModel(in, nil)
	if strings.Contains(string(out), "devhost") {
		t.Errorf("SQL endpoint not rebound:\n%s", out)
	}
	if len(outcome.Changes) != 2 {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
}

func TestRebindPartDispatchesSemanticModel(t *testing.T) {
	rb := newSemmodSQLRebinder(t)
	item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Config"}
	content := []byte(semmodSQL("devhost.datawarehouse.fabric.microsoft.com", devConfigEP))
	out, outcome := rb.RebindPart(item, "definition/expressions.tmdl", content, "")
	if strings.Contains(string(out), "devhost") || len(outcome.Changes) != 2 {
		t.Errorf("RebindPart did not rebind a SemanticModel part:\n%s\n%#v", out, outcome.Changes)
	}
	// A DataPipeline part with no baseline reference is dispatched to its own
	// pass (not the semmod one) and stays untouched — unlike devhost/devConfigEP
	// above, this host and GUID are unknown to the baseline index, so the
	// pipeline pass's own GUID and SQL-endpoint-host lookups both miss.
	plain := []byte(`Sql.Database("unknown-host.datawarehouse.fabric.microsoft.com", "11111111-2222-3333-4444-555555555555")`)
	out2, outcome2 := rb.RebindPart(LocalItem{Type: "DataPipeline", DisplayName: "P"}, "pipeline-content.json", plain, "")
	if string(out2) != string(plain) || len(outcome2.Changes) != 0 {
		t.Error("non-semmod/non-notebook part should be untouched")
	}
}

// The baked GUID is a SQLEndpoint item (indexed by name), not a Lakehouse — the
// old baseEndpoints probe missed it; ItemByGUID must resolve it by name.
func TestRebindSQLViaNameIndex(t *testing.T) {
	const (
		nameIdxDevEP  = "eeeeeeee-3333-3333-3333-333333333333"
		nameIdxTestEP = "eeeeeeee-4444-4444-4444-444444444444"
	)
	dev := fabric.Workspace{ID: "ws-dev", DisplayName: "DEV"}
	tgt := fabric.Workspace{ID: "ws-test", DisplayName: "TEST"}
	rf := &recordingFabric{fakeFabric: fakeFabric{
		workspaces: []fabric.Workspace{dev, tgt},
		itemsByWS: map[string][]fabric.Item{
			"ws-dev":  {{ID: nameIdxDevEP, DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}, {ID: "dev-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
			"ws-test": {{ID: nameIdxTestEP, DisplayName: "LH_ConfigLog", Type: "SQLEndpoint"}, {ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
		// GetLakehouseSqlEndpoint returns (host,id) per lakehouse id (host from the parent lakehouse).
		sqlByLH: map[string][2]string{"test-lh": {"test-host", nameIdxTestEP}},
	}}
	rb, err := NewRebinder(rf, "tok", []fabric.Workspace{dev}, []fabric.Workspace{tgt}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `database = Sql.Database("dev-host", "` + nameIdxDevEP + `")`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if len(out.Unresolved) != 0 {
		t.Fatalf("must resolve via NameIndex, got unresolved %+v", out.Unresolved)
	}
	if !strings.Contains(got, `"test-host"`) || !strings.Contains(got, nameIdxTestEP) {
		t.Fatalf("rewrite = %q, want test-host/%s", got, nameIdxTestEP)
	}
}

// newSemmodSharedHostRebinder wires a baseline where TWO lakehouses share one
// SQL-endpoint host (dev-host) under different endpoint ids, and a target where
// the same-named lakehouses live in different workspaces with different hosts.
// Shared-host fixture SQL-endpoint GUIDs. Production endpoint ids are always
// GUIDs, so these are obviously-fake ones rather than readable ids.
const (
	sharedDevEPOne  = "eeeeeeee-5555-5555-5555-555555555555"
	sharedDevEPTwo  = "eeeeeeee-6666-6666-6666-666666666666"
	sharedTgtEPOne  = "eeeeeeee-7777-7777-7777-777777777777"
	sharedTgtEPTwo  = "eeeeeeee-8888-8888-8888-888888888888"
	sharedUnknownEP = "eeeeeeee-9999-9999-9999-999999999999"
)

func newSemmodSharedHostRebinder(t *testing.T) *Rebinder {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DW - DEV - Data"},
			{ID: "tgt-a", DisplayName: "TGT A"},
			{ID: "tgt-b", DisplayName: "TGT B"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-data": {
				{ID: "dev-lh-one", DisplayName: "LH_One", Type: "Lakehouse"},
				{ID: sharedDevEPOne, DisplayName: "LH_One", Type: "SQLEndpoint"},
				{ID: "dev-lh-two", DisplayName: "LH_Two", Type: "Lakehouse"},
				{ID: sharedDevEPTwo, DisplayName: "LH_Two", Type: "SQLEndpoint"},
			},
			"tgt-a": {{ID: "tgt-lh-one", DisplayName: "LH_One", Type: "Lakehouse"}},
			"tgt-b": {{ID: "tgt-lh-two", DisplayName: "LH_Two", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"tgt-lh-one": {"host-a.datawarehouse.fabric.microsoft.com", sharedTgtEPOne},
			"tgt-lh-two": {"host-b.datawarehouse.fabric.microsoft.com", sharedTgtEPTwo},
		},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1], f.workspaces[2]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	return rb
}

// TestRebindSQLSharedHostDifferentTargets proves that two Sql.Database sources
// sharing the SAME baseline host whose endpoints resolve to DIFFERENT target
// hosts each get their own matched host+id pair — a global replace of the
// shared old host would funnel both into whichever target resolved first,
// pairing the second expression's host with the wrong workspace's endpoint id.
func TestRebindSQLSharedHostDifferentTargets(t *testing.T) {
	rb := newSemmodSharedHostRebinder(t)
	in := `let
	One = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "` + sharedDevEPOne + `"),
	Two = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "` + sharedDevEPTwo + `")
in
	Two`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, `Sql.Database("host-a.datawarehouse.fabric.microsoft.com", "`+sharedTgtEPOne+`")`) {
		t.Errorf("LH_One must pair host-a with tgt-ep-one:\n%s", got)
	}
	if !strings.Contains(got, `Sql.Database("host-b.datawarehouse.fabric.microsoft.com", "`+sharedTgtEPTwo+`")`) {
		t.Errorf("LH_Two must pair host-b with tgt-ep-two (not funneled into host-a):\n%s", got)
	}
	if strings.Contains(got, "dev-host") || strings.Contains(got, sharedDevEPOne) || strings.Contains(got, sharedDevEPTwo) {
		t.Errorf("baseline host/id still present:\n%s", got)
	}
	if len(out.Unresolved) != 0 {
		t.Errorf("both endpoints resolve — no unresolved expected, got %+v", out.Unresolved)
	}
	// Display must keep BOTH host rewrites (dedup is by old→new pair, not old
	// alone): 2 hosts + 2 ids = 4 changes.
	if len(out.Changes) != 4 {
		t.Fatalf("changes = %#v (want 2 hosts + 2 ids)", out.Changes)
	}
}

// TestRebindSQLUnresolvedSharesHostWithResolved proves that an UNRESOLVED
// Sql.Database expression is left byte-identical even when it shares its
// baseline host with a resolved expression — the resolved rewrite must stay
// inside its own match span.
func TestRebindSQLUnresolvedSharesHostWithResolved(t *testing.T) {
	rb := newSemmodSharedHostRebinder(t)
	unresolvedExpr := `Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "` + sharedUnknownEP + `")`
	in := `let
	One = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "` + sharedDevEPOne + `"),
	Other = ` + unresolvedExpr + `
in
	Other`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, unresolvedExpr) {
		t.Errorf("unresolved expression must be byte-identical (shared host untouched):\n%s", got)
	}
	if !strings.Contains(got, `Sql.Database("host-a.datawarehouse.fabric.microsoft.com", "`+sharedTgtEPOne+`")`) {
		t.Errorf("resolved expression not rebound:\n%s", got)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].GUID != sharedUnknownEP || out.Unresolved[0].Reason != ReasonNameUnknown {
		t.Fatalf("unresolved = %#v", out.Unresolved)
	}
}

// TestRebindSQLOverrideResolvesEndpoint proves that an override registered for
// a baked SQL-endpoint GUID unknown to the baseline index resolves the
// reference — the SQL pass must consult the override map like every other
// rebind pass (the edit-menu Info text promises exactly this).
func TestRebindSQLOverrideResolvesEndpoint(t *testing.T) {
	const (
		bakedEP        = "eeeeeeee-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		overrideTestEP = "eeeeeeee-ffff-ffff-ffff-ffffffffffff"
	)
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-config", DisplayName: "DW - DEV - Config"},
			{ID: "test-config", DisplayName: "DW - TEST - Config"},
		},
		itemsByWS: map[string][]fabric.Item{
			// Baseline knows NOTHING about the baked endpoint GUID.
			"dev-config":  {},
			"test-config": {{ID: "test-lh", DisplayName: "LH_ConfigLog", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"test-lh": {"testhost.datawarehouse.fabric.microsoft.com", overrideTestEP},
		},
	}
	overrides := map[string]Override{
		bakedEP: {ItemType: "Lakehouse", ItemName: "LH_ConfigLog"},
	}
	rb, err := NewRebinder(f, "tok",
		[]fabric.Workspace{f.workspaces[0]},
		[]fabric.Workspace{f.workspaces[1]},
		overrides)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := semmodSQL("stalehost.datawarehouse.fabric.microsoft.com", bakedEP)
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if len(out.Unresolved) != 0 {
		t.Fatalf("override should resolve the endpoint GUID, got unresolved %+v", out.Unresolved)
	}
	if !strings.Contains(got, "testhost.datawarehouse.fabric.microsoft.com") || !strings.Contains(got, overrideTestEP) {
		t.Errorf("override not applied:\n%s", got)
	}
	if strings.Contains(got, "stalehost") || strings.Contains(got, bakedEP) {
		t.Errorf("baseline endpoint still present:\n%s", got)
	}
}

// --- Sql.Database NAME-form branch (guidShapeRe rejects the id) ---
//
// Sql.Database("host", "LH_Gold") carries the database NAME (equal to the
// lakehouse name, and the same in the target), not a baked endpoint GUID.
// resolveNameReason resolves it directly in the target index (override-first,
// like every other rebind pass) and only the host is rewritten — the name
// argument is untouched.

// TestRebindSQLNameFormRewritesHostOnly proves the happy path: the target has
// a lakehouse named LH_Gold, so only the host is rewritten and the name stays
// byte-identical.
func TestRebindSQLNameFormRewritesHostOnly(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-data", DisplayName: "TGT Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"tgt-data": {{ID: "tgt-gold-lh", DisplayName: "LH_Gold", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"tgt-gold-lh": {"tgt-host.datawarehouse.fabric.microsoft.com", "eeeeeeee-dddd-dddd-dddd-dddddddddddd"},
		},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let Source = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Gold") in Source`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if !strings.Contains(got, `Sql.Database("tgt-host.datawarehouse.fabric.microsoft.com", "LH_Gold")`) {
		t.Errorf("host not rebound / name not preserved byte-identical:\n%s", got)
	}
	if len(out.Unresolved) != 0 {
		t.Fatalf("expected no unresolved, got %+v", out.Unresolved)
	}
	if len(out.Changes) != 1 || out.Changes[0].Kind != "SQL endpoint" {
		t.Fatalf("changes = %#v (want exactly one SQL endpoint change, for the host)", out.Changes)
	}
}

// TestRebindSQLNameFormUnresolvedNotInTarget proves that a database name absent
// from every target workspace is left byte-identical and reported unresolved
// with ItemType "SQL database (by name)" and Reason not-in-target.
func TestRebindSQLNameFormUnresolvedNotInTarget(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-data", DisplayName: "TGT Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"tgt-data": {}, // LH_Ghost exists nowhere in the target
		},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let Source = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Ghost") in Source`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if got != in {
		t.Errorf("unresolved name-form ref must leave content unchanged:\n%s", got)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].ItemType != "SQL database (by name)" || out.Unresolved[0].Reason != ReasonNotInTarget {
		t.Fatalf("unresolved = %#v (want one SQL database (by name) with ReasonNotInTarget)", out.Unresolved)
	}
}

// TestRebindSQLNameFormAmbiguous proves that a database name present in TWO
// target workspaces (so the target NameIndex can't place it) is left
// byte-identical and reported unresolved with Reason ambiguous.
func TestRebindSQLNameFormAmbiguous(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-a", DisplayName: "TGT A"},
			{ID: "tgt-b", DisplayName: "TGT B"},
		},
		itemsByWS: map[string][]fabric.Item{
			"tgt-a": {{ID: "tgt-a-gold-lh", DisplayName: "LH_Gold", Type: "Lakehouse"}},
			"tgt-b": {{ID: "tgt-b-gold-lh", DisplayName: "LH_Gold", Type: "Lakehouse"}},
		},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1], f.workspaces[2]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let Source = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Gold") in Source`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if got != in {
		t.Errorf("ambiguous name-form ref must leave content unchanged:\n%s", got)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].Reason != ReasonAmbiguous {
		t.Fatalf("unresolved = %#v (want one with ReasonAmbiguous)", out.Unresolved)
	}
}

// TestRebindSQLNameFormOverrideTakesPrecedence proves that an override keyed by
// the literal database name ("LH_Gold") redirects resolution to a
// differently-named target lakehouse — override-first precedence, mirroring
// resolveGUIDReason's precedence for GUID-form refs.
func TestRebindSQLNameFormOverrideTakesPrecedence(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-data", DisplayName: "TGT Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			// The target has no lakehouse literally named LH_Gold; the override
			// redirects the reference to LH_Renamed instead.
			"tgt-data": {{ID: "tgt-renamed-lh", DisplayName: "LH_Renamed", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"tgt-renamed-lh": {"tgt-host.datawarehouse.fabric.microsoft.com", "eeeeeeee-cccc-cccc-cccc-cccccccccccc"},
		},
	}
	overrides := map[string]Override{
		"LH_Gold": {ItemType: "Lakehouse", ItemName: "LH_Renamed"},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1]}, overrides)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let Source = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Gold") in Source`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if len(out.Unresolved) != 0 {
		t.Fatalf("override should resolve the name, got unresolved %+v", out.Unresolved)
	}
	if !strings.Contains(got, `Sql.Database("tgt-host.datawarehouse.fabric.microsoft.com", "LH_Gold")`) {
		t.Errorf("override not applied / name not preserved byte-identical:\n%s", got)
	}
}

// TestRebindSQLUnresolvedDedupsCount proves that AddUnresolved's dedup applies
// end-to-end through rebindSQLSources: the SAME broken name-form reference
// appearing in three expressions collapses into ONE UnresolvedRef with
// Count=3, not three identical entries.
func TestRebindSQLUnresolvedDedupsCount(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-data", DisplayName: "TGT Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"tgt-data": {}, // LH_Ghost exists nowhere in the target
		},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let
	A = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Ghost"),
	B = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Ghost"),
	C = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "LH_Ghost")
in
	C`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if got != in {
		t.Errorf("unresolved refs must leave content unchanged:\n%s", got)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0].Count != 3 {
		t.Fatalf("unresolved = %#v (want exactly one ref with Count=3)", out.Unresolved)
	}
}

// TestRebindSQLNameFormMatchesLakehouseCaseInsensitively pins the case rule
// for the NAME form. SQL resolves database names case-insensitively (a model
// with "lh_gold" connects fine to the endpoint of a lakehouse named LH_Gold),
// and Fabric binds a model's data source to a cloud connection by EXACT string
// match — so the literal in git must be preserved byte-for-byte, while the
// lakehouse lookup that finds the target host must ignore case. Before this
// test, "lh_gold" missed the exact-match name index, the source went
// unresolved, and the deployed model kept pointing at the baseline host.
func TestRebindSQLNameFormMatchesLakehouseCaseInsensitively(t *testing.T) {
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DEV Data"},
			{ID: "tgt-data", DisplayName: "TGT Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"tgt-data": {{ID: "tgt-gold-lh", DisplayName: "LH_Gold", Type: "Lakehouse"}},
		},
		sqlByLH: map[string][2]string{
			"tgt-gold-lh": {"tgt-host.datawarehouse.fabric.microsoft.com", "eeeeeeee-dddd-dddd-dddd-dddddddddddd"},
		},
	}
	rb, err := NewRebinder(f, "tok", []fabric.Workspace{f.workspaces[0]}, []fabric.Workspace{f.workspaces[1]}, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	in := `let Source = Sql.Database("dev-host.datawarehouse.fabric.microsoft.com", "lh_gold") in Source`
	var out RebindOutcome
	got := rb.rebindSQLSources(in, &out)
	if len(out.Unresolved) != 0 {
		t.Fatalf("lowercase database name must resolve the LH_Gold lakehouse, got unresolved %+v", out.Unresolved)
	}
	if !strings.Contains(got, `Sql.Database("tgt-host.datawarehouse.fabric.microsoft.com", "lh_gold")`) {
		t.Errorf("host not rebound, or database name not preserved byte-identical:\n%s", got)
	}
	if strings.Contains(got, "LH_Gold") {
		t.Errorf("database literal must keep the casing from git, never adopt the lakehouse display name:\n%s", got)
	}
}

// M parameter host rebind. A model can keep its SQL endpoint host in an M
// parameter and read Sql.Database(SqlEndpoint, "LH_Bronze"); the Sql.Database
// pass can't see that host, so without this pass the target model kept reading
// from the baseline environment after deploy. Example: a parameter holding the
// DEV host of LH_Bronze deploys to TEST holding the TEST host of LH_Bronze.

const (
	paramDevHost = "aaa111-dev.datawarehouse.fabric.microsoft.com"
	paramTstHost = "bbb222-test.datawarehouse.fabric.microsoft.com"
)

func paramRebinder() *Rebinder {
	rb := pipelineRebinderGUIDs()
	rb.client = &fakeFabric{sqlByLH: map[string][2]string{
		gLHDev: {paramDevHost, "ep-dev"},
		gLHTst: {paramTstHost, "ep-test"},
	}}
	return rb
}

func paramExpressions(host string) string {
	return "expression SqlEndpoint = \"" + host + "\" meta [IsParameterQuery=true, Type=\"Text\", IsParameterQueryRequired=true]\n" +
		"\tlineageTag: 00000000-aaaa-4bbb-8ccc-000000000001\n\n" +
		"\tannotation PBI_ResultType = Text\n"
}

func substituteParamModel(t *testing.T, rb *Rebinder, content string) (string, RebindOutcome) {
	t.Helper()
	item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", Parts: []Part{
		{Path: "definition/expressions.tmdl", Content: []byte(content)},
	}}
	parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
	if err != nil {
		t.Fatal(err)
	}
	return string(parts["definition/expressions.tmdl"]), outcome
}

func TestRebindHostParameterRewritten(t *testing.T) {
	rb := paramRebinder()
	got, outcome := substituteParamModel(t, rb, paramExpressions(paramDevHost))
	if got != paramExpressions(paramTstHost) {
		t.Fatalf("parameter host not rewritten:\n%s", got)
	}
	if len(outcome.Changes) != 1 || !reflect.DeepEqual(outcome.Changes[0], RebindChange{Kind: "SQL endpoint", Name: "LH_Bronze", Old: paramDevHost, New: paramTstHost, Items: []ItemRef{{Name: "SM_Sales", Type: "SemanticModel"}}}) {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("rewritten host must not be reported as a leftover: %#v", outcome.Unresolved)
	}
}

// Only the parameter's value is rewritten: the same host in a /// description,
// a // comment or a non-parameter expression is arbitrary text and stays.
func TestRebindHostParameterLeavesOtherTextAlone(t *testing.T) {
	rb := paramRebinder()
	in := "/// Points at " + paramDevHost + " in DEV\n" +
		"// " + paramDevHost + "\n" +
		paramExpressions(paramDevHost) + "\n" +
		"expression NotAParam = \"" + paramDevHost + "\" meta [IsParameterQuery=false]\n" +
		"expression Prefixed = \"tcp:" + paramDevHost + "\" meta [IsParameterQuery=true, Type=\"Text\"]\n"
	var out RebindOutcome
	got := rb.rebindHostParameters(in, &out, nil)
	want := strings.Replace(in, "expression SqlEndpoint = \""+paramDevHost, "expression SqlEndpoint = \""+paramTstHost, 1)
	if got != want {
		t.Fatalf("only the parameter value may change:\n%s", got)
	}
	if len(out.Changes) != 1 {
		t.Fatalf("changes = %#v", out.Changes)
	}
}

func TestRebindHostParameterUnknownHostUntouched(t *testing.T) {
	rb := paramRebinder()
	in := paramExpressions("someone-elses.datawarehouse.fabric.microsoft.com")
	got, outcome := substituteParamModel(t, rb, in)
	if got != in || len(outcome.Changes) != 0 || len(outcome.Unresolved) != 0 {
		t.Fatalf("unknown host must be untouched and unreported: %s %#v %#v", got, outcome.Changes, outcome.Unresolved)
	}
}

// No same-named lakehouse in the target: the value stays, and the leftover scan
// (not this pass) reports it exactly once.
func TestRebindHostParameterNotInTargetLeavesLeftover(t *testing.T) {
	rb := &Rebinder{
		baseline: idx(IndexedItem{Name: "LH_OnlyDev", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev}),
		target:   idx(), overrides: map[string]Override{},
		wsMap: map[string]string{}, wsAmbiguous: map[string]bool{}, baselineWSNames: map[string]string{},
		client: &fakeFabric{sqlByLH: map[string][2]string{gLHDev: {paramDevHost, "ep-dev"}}},
	}
	in := paramExpressions(paramDevHost)
	got, outcome := substituteParamModel(t, rb, in)
	if got != in || len(outcome.Changes) != 0 {
		t.Fatalf("host with no target lakehouse must be left untouched: %s %#v", got, outcome.Changes)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].Location != LocationLeftover || outcome.Unresolved[0].GUID != paramDevHost {
		t.Fatalf("want one leftover warning for the host: %#v", outcome.Unresolved)
	}
}

func TestRebindHostParameterIdempotent(t *testing.T) {
	rb := paramRebinder()
	once, _ := rb.RebindSemanticModel([]byte(paramExpressions(paramDevHost)), nil)
	twice, outcome := rb.RebindSemanticModel(once, nil)
	if string(twice) != string(once) || len(outcome.Changes) != 0 {
		t.Fatalf("second run must be a no-op: %s %#v", twice, outcome.Changes)
	}
}

// Shared endpoint (both envs read the same host): nothing to rewrite.
func TestRebindHostParameterSharedEndpointUntouched(t *testing.T) {
	rb := paramRebinder()
	rb.client = &fakeFabric{sqlByLH: map[string][2]string{
		gLHDev: {paramDevHost, "ep-dev"},
		gLHTst: {paramDevHost, "ep-dev"},
	}}
	in := paramExpressions(paramDevHost)
	got, outcome := substituteParamModel(t, rb, in)
	if got != in || len(outcome.Changes) != 0 || len(outcome.Unresolved) != 0 {
		t.Fatalf("shared endpoint must be untouched: %s %#v %#v", got, outcome.Changes, outcome.Unresolved)
	}
}

// Every lakehouse in a workspace shares one SQL endpoint host, so the host
// alone can't say which lakehouse a parameter serves. The database name in
// Sql.Database(SqlEndpoint, "lh_gold"), in the table parts, can. Here
// LH_Bronze and LH_Gold share the DEV host but have different TEST hosts, so
// picking the wrong owner would rewrite to the wrong endpoint.
const (
	gLHGoldDev  = "c0de0000-1111-4222-8333-777788889999"
	gLHGoldTst  = "f00d0000-1111-4222-8333-777788889999"
	goldTstHost = "ccc333-gold.datawarehouse.fabric.microsoft.com"
)

func sharedHostRebinder(targetGold bool) *Rebinder {
	rb := pipelineRebinderGUIDs()
	rb.baseline = idx(
		IndexedItem{Name: "LH_Bronze", Type: "Lakehouse", GUID: gLHDev, WorkspaceID: gWSDev},
		IndexedItem{Name: "LH_Gold", Type: "Lakehouse", GUID: gLHGoldDev, WorkspaceID: gWSDev},
	)
	tgt := []IndexedItem{{Name: "LH_Bronze", Type: "Lakehouse", GUID: gLHTst, WorkspaceID: gWSTst}}
	if targetGold {
		tgt = append(tgt, IndexedItem{Name: "LH_Gold", Type: "Lakehouse", GUID: gLHGoldTst, WorkspaceID: gWSTst})
	}
	rb.target = idx(tgt...)
	rb.client = &fakeFabric{sqlByLH: map[string][2]string{
		gLHDev:     {paramDevHost, "ep-dev"},
		gLHGoldDev: {paramDevHost, "ep-gold-dev"},
		gLHTst:     {paramTstHost, "ep-test"},
		gLHGoldTst: {goldTstHost, "ep-gold-test"},
	}}
	return rb
}

const goldTable = "table Dato\n\tpartition Dato = m\n\t\tsource =\n\t\t\t\tlet\n\t\t\t\t    Source = Sql.Database(SqlEndpoint, \"lh_gold\")\n\t\t\t\tin\n\t\t\t\t    Source\n"

func substituteSharedHostModel(t *testing.T, rb *Rebinder) (map[string][]byte, RebindOutcome) {
	t.Helper()
	item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", Parts: []Part{
		{Path: "definition/expressions.tmdl", Content: []byte(paramExpressions(paramDevHost))},
		{Path: "definition/tables/Dato.tmdl", Content: []byte(goldTable)},
	}}
	parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
	if err != nil {
		t.Fatal(err)
	}
	return parts, outcome
}

func TestRebindHostParameterUsesDatabaseName(t *testing.T) {
	parts, outcome := substituteSharedHostModel(t, sharedHostRebinder(true))
	if got := string(parts["definition/expressions.tmdl"]); got != paramExpressions(goldTstHost) {
		t.Fatalf("parameter must point at LH_Gold's target endpoint:\n%s", got)
	}
	if string(parts["definition/tables/Dato.tmdl"]) != goldTable {
		t.Fatalf("table part must be untouched:\n%s", parts["definition/tables/Dato.tmdl"])
	}
	if len(outcome.Changes) != 1 || !reflect.DeepEqual(outcome.Changes[0], RebindChange{Kind: "SQL endpoint", Name: "LH_Gold", Old: paramDevHost, New: goldTstHost, Items: []ItemRef{{Name: "SM_Sales", Type: "SemanticModel"}}}) {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}

// The database the parameter serves is missing in the target. Another
// lakehouse sharing the baseline host does exist there, but its endpoint is
// not what the model reads, so the value stays and the leftover scan warns.
func TestRebindHostParameterDatabaseNotInTarget(t *testing.T) {
	parts, outcome := substituteSharedHostModel(t, sharedHostRebinder(false))
	if got := string(parts["definition/expressions.tmdl"]); got != paramExpressions(paramDevHost) {
		t.Fatalf("parameter must stay when its database is missing in the target:\n%s", got)
	}
	if len(outcome.Changes) != 0 {
		t.Fatalf("changes = %#v", outcome.Changes)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].GUID != paramDevHost {
		t.Fatalf("want one leftover warning for the host: %#v", outcome.Unresolved)
	}
}

// One parameter serving several databases. A parameter holds one host, so it
// can only be rewritten when every database it serves lands on the same target
// endpoint (lakehouses in one workspace share it). If the target splits them
// across endpoints, no single value is right: it stays and the leftover scan
// warns.
func TestRebindHostParameterSeveralDatabases(t *testing.T) {
	const bronzeTable = "table Prosjekt\n\tpartition Prosjekt = m\n\t\tsource = Sql.Database(SqlEndpoint, \"LH_Bronze\")\n"
	run := func(goldHost string) (string, RebindOutcome) {
		rb := sharedHostRebinder(true)
		rb.client.(*fakeFabric).sqlByLH[gLHGoldTst] = [2]string{goldHost, "ep-gold-test"}
		item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", Parts: []Part{
			{Path: "definition/expressions.tmdl", Content: []byte(paramExpressions(paramDevHost))},
			{Path: "definition/tables/Dato.tmdl", Content: []byte(goldTable)},
			{Path: "definition/tables/Prosjekt.tmdl", Content: []byte(bronzeTable)},
		}}
		parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
		if err != nil {
			t.Fatal(err)
		}
		return string(parts["definition/expressions.tmdl"]), outcome
	}

	got, outcome := run(paramTstHost) // both on the same TEST endpoint
	if got != paramExpressions(paramTstHost) {
		t.Fatalf("shared target endpoint must be written:\n%s", got)
	}
	if len(outcome.Changes) != 1 || outcome.Changes[0].Name != "LH_Bronze, LH_Gold" || len(outcome.Unresolved) != 0 {
		t.Fatalf("changes = %#v unresolved = %#v", outcome.Changes, outcome.Unresolved)
	}

	got, outcome = run(goldTstHost) // TEST splits them across endpoints
	if got != paramExpressions(paramDevHost) || len(outcome.Changes) != 0 {
		t.Fatalf("split target endpoints must leave the value: %s %#v", got, outcome.Changes)
	}
	if len(outcome.Unresolved) != 1 || outcome.Unresolved[0].GUID != paramDevHost {
		t.Fatalf("want one leftover warning for the host: %#v", outcome.Unresolved)
	}
}

// A parameter whose Sql.Database calls name no literal database (here the
// database is a parameter too) falls back to the host's owners, and resolves
// the same way on every run.
func TestRebindHostParameterFallbackIsDeterministic(t *testing.T) {
	const dynTable = "table Dato\n\tpartition Dato = m\n\t\tsource = Sql.Database(SqlEndpoint, DatabaseName)\n"
	for i := 0; i < 20; i++ {
		rb := sharedHostRebinder(true)
		rb.target = idx(IndexedItem{Name: "LH_Gold", Type: "Lakehouse", GUID: gLHGoldTst, WorkspaceID: gWSTst})
		item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", Parts: []Part{
			{Path: "definition/expressions.tmdl", Content: []byte(paramExpressions(paramDevHost))},
			{Path: "definition/tables/Dato.tmdl", Content: []byte(dynTable)},
		}}
		parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(parts["definition/expressions.tmdl"]); got != paramExpressions(goldTstHost) {
			t.Fatalf("run %d: fallback must follow the owner present in the target:\n%s", i, got)
		}
		if len(outcome.Changes) != 1 || outcome.Changes[0].Name != "LH_Gold" {
			t.Fatalf("run %d: changes = %#v", i, outcome.Changes)
		}
	}
}

// A custom substitution can rename the database a table reads
// ("lh_gold_dev" → "lh_gold"). The parameter must be resolved from the
// substituted text that deploys, not from git's raw text.
func TestRebindHostParameterSeesSubstitutedDatabaseName(t *testing.T) {
	rb := sharedHostRebinder(true)
	rb.SetSubstitutions([]Substitution{{FindValue: `"lh_gold_dev"`, Literal: `"lh_gold"`, FilePath: "definition/tables/*"}})
	devTable := strings.Replace(goldTable, `"lh_gold"`, `"lh_gold_dev"`, 1)
	item := LocalItem{Type: "SemanticModel", DisplayName: "SM_Sales", Parts: []Part{
		{Path: "definition/expressions.tmdl", Content: []byte(paramExpressions(paramDevHost))},
		{Path: "definition/tables/Dato.tmdl", Content: []byte(devTable)},
	}}
	parts, outcome, err := SubstituteParts(item, map[string]string{}, nil, rb, gWSTst)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(parts["definition/expressions.tmdl"]); got != paramExpressions(goldTstHost) {
		t.Fatalf("parameter must resolve through the substituted database name:\n%s", got)
	}
	if len(outcome.Unresolved) != 0 {
		t.Fatalf("unresolved = %#v", outcome.Unresolved)
	}
}
