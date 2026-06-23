package deploy

import (
	"strings"
	"sync"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// concurrencyFixture builds a Resolver and a Rebinder that SHARE the same fake
// client and that several goroutines hit at once, so their lazy caches
// (Resolver.wsByName/itemsWS, Rebinder.baseEndpoints/targetEndpoint) are
// populated under contention. The baseline and target both expose the same
// lakehouse name (LH_Silver) with a SQL endpoint, so every worker drives the
// same baseEndpointLookup + targetEndpointFor fill.
func concurrencyFixture(t *testing.T) (*Resolver, *Rebinder) {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DP - DEV - Data"},
			{ID: "test-data", DisplayName: "DP - TEST - Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			"dev-data":  {{ID: "dev-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}},
			"test-data": {{ID: "test-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}},
		},
		// Both lakehouses expose SQL endpoints so the SQL rebind path populates
		// baseEndpoints (baseline) and targetEndpoint (target).
		sqlByLH: map[string][2]string{
			"dev-silver-lh":  {"dev-silver.datawarehouse.fabric.microsoft.com", "dev-ep"},
			"test-silver-lh": {"test-silver.datawarehouse.fabric.microsoft.com", "test-ep"},
		},
	}
	baselineWS := []fabric.Workspace{f.workspaces[0]}
	targetWS := []fabric.Workspace{f.workspaces[1]}
	rb, err := NewRebinder(f, "tok", baselineWS, targetWS, nil)
	if err != nil {
		t.Fatalf("NewRebinder: %v", err)
	}
	target := fabric.Workspace{ID: "test-data", DisplayName: "DP - TEST - Data"}
	resolver := NewResolver(f, "tok", target)
	return resolver, rb
}

// semModelWithSQLSource is a Direct-Lake-on-SQL semantic-model part baked
// against the DEV endpoint; rebind should swap host+id to the TEST endpoint.
// Uses the on-disk TMDL connection shape Sql.Database("host", "id").
func semModelWithSQLSource() []byte {
	return []byte(`let Source = Sql.Database("dev-silver.datawarehouse.fabric.microsoft.com", "dev-ep") in Source`)
}

// TestSubstitutePartsConcurrentSharedCaches drives SubstituteParts from many
// goroutines against one shared Resolver and Rebinder, with items that exercise
// BOTH lazy caches at once:
//   - a semantic-model part whose SQL source forces baseEndpointLookup +
//     targetEndpointFor (Rebinder caches), and
//   - a find_replace whose replacement is a $items dynamic var forcing the
//     Resolver's wsByName/itemsWS fill.
//
// Run under `go test -race`. Before the mutexes were added this reliably
// tripped the race detector (concurrent map writes to the lazy caches); with
// the guards it must be clean. A non-concurrent cache fill would prove nothing,
// so every worker hits the SAME workspace/endpoint lookups.
func TestSubstitutePartsConcurrentSharedCaches(t *testing.T) {
	resolver, rb := concurrencyFixture(t)

	params := Parameters{FindReplace: []FindReplace{{
		FindValue:    "__ENDPOINT__",
		ReplaceValue: map[string]string{"TEST": "$items.Lakehouse.LH_Silver.$sqlendpoint"},
	}}}

	const workers = 32
	var wg sync.WaitGroup
	errs := make([]error, workers)
	hosts := make([]string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			item := LocalItem{
				Type:        "SemanticModel",
				DisplayName: "Model_Sales",
				Parts: []Part{
					// Part 1: SQL source -> Rebinder endpoint caches.
					{Path: "definition/expressions.tmdl", Content: semModelWithSQLSource()},
					// Part 2: $items dynamic var -> Resolver caches.
					{Path: "definition.pbism", Content: []byte("endpoint=__ENDPOINT__")},
				},
			}
			parts, _, err := SubstituteParts(item, "TEST", params, map[string]string{}, resolver, rb)
			errs[w] = err
			if err == nil {
				hosts[w] = string(parts["definition.pbism"])
			}
		}(w)
	}
	wg.Wait()

	for w := 0; w < workers; w++ {
		if errs[w] != nil {
			t.Fatalf("worker %d: %v", w, errs[w])
		}
		// Resolver fill must have produced the TARGET endpoint host deterministically.
		if want := "endpoint=test-silver.datawarehouse.fabric.microsoft.com"; hosts[w] != want {
			t.Fatalf("worker %d: resolver gave %q, want %q", w, hosts[w], want)
		}
	}
}

// TestRebinderConcurrentEndpointCaches hammers RebindSemanticModel from many
// goroutines so the Rebinder's baseEndpoints + targetEndpoint maps are filled
// concurrently. Under -race this must be clean, and every worker must produce
// the same target-endpoint rewrite.
func TestRebinderConcurrentEndpointCaches(t *testing.T) {
	_, rb := concurrencyFixture(t)

	const workers = 32
	var wg sync.WaitGroup
	outs := make([]string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			out, _ := rb.RebindSemanticModel(semModelWithSQLSource())
			outs[w] = string(out)
		}(w)
	}
	wg.Wait()

	for w := 0; w < workers; w++ {
		if !strings.Contains(outs[w], "test-silver.datawarehouse.fabric.microsoft.com") ||
			!strings.Contains(outs[w], "test-ep") ||
			strings.Contains(outs[w], "dev-ep") {
			t.Fatalf("worker %d: SQL source not rebound to target endpoint: %s", w, outs[w])
		}
	}
}
