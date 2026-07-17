package cmd

import (
	"regexp"
	"strings"
	"testing"
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
	for _, ws := range demoWorkspaces() {
		env, _, ok := demoEnvOfWS(ws.ID)
		if !ok {
			t.Fatalf("workspace %s has no env mapping", ws.DisplayName)
		}
		items, ok := demoItems(ws.ID)
		if !ok || len(items) == 0 {
			t.Fatalf("workspace %s has no items", ws.DisplayName)
		}
		for _, it := range items {
			if def := demoDefinition(env, it); def == nil {
				t.Fatalf("nil definition for %s/%s in %s", it.Type, it.DisplayName, ws.DisplayName)
			}
		}
	}

	// Every DEV GUID baked into the seeded repo content must exist as an item
	// (or workspace) in the fake DEV tenant, so the baseline index can name it.
	known := map[string]bool{}
	for _, env := range demoEnvs {
		for _, wsName := range []string{demoConfigWS(env), demoSemModWS(env)} {
			wsID := demoGUID("workspace", wsName)
			known[wsID] = true
			items, _ := demoItems(wsID)
			for _, it := range items {
				known[it.ID] = true
			}
		}
	}
	guidAnywhere := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
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
