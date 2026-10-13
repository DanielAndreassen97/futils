package cmd

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/fabric"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestDemoGUIDShape: every derived GUID must be a valid RFC 4122 v4-shaped
// UUID (the deploy flow's regexes only match canonical GUIDs) and stable
// across calls — the seeded repo and the fake tenant must agree.
func TestDemoGUIDShape(t *testing.T) {
	g1 := demoGUID("item", "Lakehouse", "LH_Bronze", "DEV")
	g2 := demoGUID("item", "Lakehouse", "LH_Bronze", "DEV")
	if g1 != g2 {
		t.Fatalf("demoGUID not deterministic: %s vs %s", g1, g2)
	}
	if !uuidRe.MatchString(g1) {
		t.Fatalf("demoGUID %q is not a canonical v4 UUID", g1)
	}
	if g1 == demoGUID("item", "Lakehouse", "LH_Bronze", "TEST") {
		t.Fatal("different envs must yield different GUIDs")
	}
}

// TestDemoTenantCoherent: every item listed in every demo workspace must
// serve a definition, and the seeded repo's GUIDs must resolve in the fake
// tenant's DEV baseline (otherwise the rebind pass would report unresolved
// references in a dataset designed to resolve cleanly).
func TestDemoTenantCoherent(t *testing.T) {
	// One walk over the tenant: every item must serve a definition, and every
	// workspace/item GUID feeds the known-set the repo-content check uses.
	known := map[string]bool{}
	for _, ws := range demoWorkspaces() {
		env, _, ok := demoEnvOfWS(ws.ID)
		if !ok {
			t.Fatalf("workspace %s has no env mapping", ws.DisplayName)
		}
		items, ok := demoItems(ws.ID)
		if !ok || len(items) == 0 {
			t.Fatalf("workspace %s has no items", ws.DisplayName)
		}
		known[ws.ID] = true
		for _, it := range items {
			if def := demoDefinition(env, it); def == nil {
				t.Fatalf("nil definition for %s/%s in %s", it.Type, it.DisplayName, ws.DisplayName)
			}
			known[it.ID] = true
		}
	}

	// logicalId references (e.g. PL_master invoking PL_refresh_sales) are
	// resolved by the deploy's logicalId→GUID substitution, not the baseline
	// index — collect them from the repo's .platform files as known too.
	guidAnywhere := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	for path, content := range demoRepoFiles() {
		if strings.HasSuffix(path, ".platform") {
			for _, g := range guidAnywhere.FindAllString(content, -1) {
				known[g] = true
			}
		}
	}

	// Every other DEV GUID baked into the seeded repo content must exist as an
	// item (or workspace) in the fake DEV tenant, so the baseline index can
	// name it.
	for path, content := range demoRepoFiles() {
		if strings.HasSuffix(path, ".platform") {
			continue // logicalIds live only in git, never in the tenant
		}
		for _, g := range guidAnywhere.FindAllString(content, -1) {
			if strings.Contains(content, "lineageTag: "+g) {
				continue // lineage tags are content, not references
			}
			if !known[g] {
				t.Errorf("%s bakes GUID %s that no demo workspace or item carries", path, g)
			}
		}
	}
}

// TestDemoBulkItem: items in a bulk payload are identified via their .platform
// part's metadata — exactly one detail per item, any Fabric type.
func TestDemoBulkItem(t *testing.T) {
	platform := func(typ, name string) fabric.DefinitionPart {
		return fabric.DefinitionPart{
			Path: "/Backend/" + name + "." + typ + "/.platform",
			Payload: base64.StdEncoding.EncodeToString([]byte(
				`{"metadata":{"type":"` + typ + `","displayName":"` + name + `"}}`)),
		}
	}
	name, typ, ok := demoBulkItem(platform("DataPipeline", "pl_orchestrate"))
	if !ok || name != "pl_orchestrate" || typ != "DataPipeline" {
		t.Fatalf("got %q %q %v", name, typ, ok)
	}
	content := fabric.DefinitionPart{Path: "/Backend/nb_x.Notebook/notebook-content.py"}
	if _, _, ok := demoBulkItem(content); ok {
		t.Fatal("non-.platform parts must not resolve to an item")
	}
}

// TestDemoSQLModelsRebindClean runs the deploy's rebind and diff path DEV→TEST
// on the demo's two Direct Lake on SQL models: the literal Sql.Database form
// (Finance Model) and the host-in-an-M-parameter form (Budget Model). Both
// differ between environments only in baked endpoint values, so after
// rebinding each must compare as Unchanged with no leftover warning. If either
// rebind regresses, a demo deploy shows a bogus Changed row and a baseline
// warning instead of the story the demo is meant to tell.
func TestDemoSQLModelsRebindClean(t *testing.T) {
	api := newDemoClient()
	var dev, test []fabric.Workspace
	for _, ws := range demoWorkspaces() {
		switch env, _, _ := demoEnvOfWS(ws.ID); env {
		case "DEV":
			dev = append(dev, ws)
		case "TEST":
			test = append(test, ws)
		}
	}
	rb, err := deploy.NewRebinder(api, "tok", dev, test, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := demoRepoFiles()
	for _, name := range []string{"Finance Model", "Budget Model"} {
		prefix := "Frontend/" + name + ".SemanticModel/"
		item := deploy.LocalItem{Type: "SemanticModel", DisplayName: name}
		for _, p := range []string{"definition/expressions.tmdl", "definition/model.tmdl"} {
			item.Parts = append(item.Parts, deploy.Part{Path: p, Content: []byte(repo[prefix+p])})
		}
		local, outcome, err := deploy.SubstituteParts(item, map[string]string{}, nil, rb, demoGUID("workspace", demoSemModWS("TEST")))
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Unresolved) != 0 {
			t.Errorf("%s: unresolved after rebind: %#v", name, outcome.Unresolved)
		}
		if len(outcome.Changes) == 0 {
			t.Errorf("%s: no rebind change recorded", name)
		}
		// LH_Bronze and LH_Silver share the demo's endpoint host, so the
		// change must name the lakehouse the model reads, not either owner.
		for _, c := range outcome.Changes {
			if c.Kind == "SQL endpoint" && c.Name != "LH_Silver" {
				t.Errorf("%s: SQL endpoint change names %q, want LH_Silver", name, c.Name)
			}
		}
		deployed := demoDefinition("TEST", fabric.Item{Type: "SemanticModel", DisplayName: name})
		if diffs := deploy.DiffParts(local, deployed); len(diffs) != 0 {
			t.Errorf("%s: compares as Changed after rebind: %#v", name, diffs)
		}
	}
}
