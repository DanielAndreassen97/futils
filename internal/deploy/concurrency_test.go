package deploy

import (
	"strings"
	"sync"
	"testing"

	"github.com/DanielAndreassen97/futils/internal/fabric"
)

// concurrencyFixture builds a Resolver and a Rebinder that SHARE the same fake
// client and that several goroutines hit at once, so their lazy caches
// (Resolver.wsByName/itemsWS, Rebinder.targetEndpoint) are populated under
// contention. The baseline and target both expose the same lakehouse name
// (LH_Silver) with a SQL endpoint, so every worker drives the same
// ItemByGUID + targetEndpointFor fill.
func concurrencyFixture(t *testing.T) (*Resolver, *Rebinder) {
	t.Helper()
	f := &fakeFabric{
		workspaces: []fabric.Workspace{
			{ID: "dev-data", DisplayName: "DP - DEV - Data"},
			{ID: "test-data", DisplayName: "DP - TEST - Data"},
		},
		itemsByWS: map[string][]fabric.Item{
			// dev-ep is the baked SQL-endpoint item (indexed by name, resolved via
			// ItemByGUID) for the parent lakehouse dev-silver-lh.
			"dev-data":  {{ID: "dev-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}, {ID: "dev-ep", DisplayName: "LH_Silver", Type: "SQLEndpoint"}},
			"test-data": {{ID: "test-silver-lh", DisplayName: "LH_Silver", Type: "Lakehouse"}},
		},
		// The target lakehouse exposes its SQL endpoint so the SQL rebind path
		// populates the Rebinder's targetEndpoint cache.
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
// the shared lazy cache at once:
//   - a semantic-model part whose SQL source resolves the baked endpoint via
//     ItemByGUID (read-only, build-once) and then targetEndpointFor (the
//     Rebinder's one remaining lazy cache), and
//   - a custom substitution that uses the Rebinder's target-attribute lookup
//     (sqlendpoint), forcing the same targetEndpoint cache fill.
//
// Run under `go test -race`. Before the mutex was added this reliably
// tripped the race detector (concurrent map writes to the lazy cache); with
// the guard it must be clean. A non-concurrent cache fill would prove nothing,
// so every worker hits the SAME workspace/endpoint lookups.
func TestSubstitutePartsConcurrentSharedCaches(t *testing.T) {
	resolver, rb := concurrencyFixture(t)
	rb.SetSubstitutions([]Substitution{{
		FindValue:  "__ENDPOINT__",
		TargetType: "Lakehouse",
		TargetName: "LH_Silver",
		Attr:       "sqlendpoint",
	}})

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
					// Part 1: SQL source -> Rebinder SQL endpoint caches.
					{Path: "definition/expressions.tmdl", Content: semModelWithSQLSource()},
					// Part 2: custom substitution -> Rebinder targetEndpoint cache.
					{Path: "definition.pbism", Content: []byte("endpoint=__ENDPOINT__")},
				},
			}
			parts, _, err := SubstituteParts(item, map[string]string{}, resolver, rb)
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
		// Custom substitution must have resolved to the target lakehouse SQL endpoint host.
		if want := "endpoint=test-silver.datawarehouse.fabric.microsoft.com"; hosts[w] != want {
			t.Fatalf("worker %d: substitution gave %q, want %q", w, hosts[w], want)
		}
	}
}

// TestRebinderConcurrentEndpointCaches hammers RebindSemanticModel from many
// goroutines so the Rebinder's targetEndpoint map is filled concurrently.
// Under -race this must be clean, and every worker must produce the same
// target-endpoint rewrite.
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
