package schemacompare

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeAPI struct {
	tables   map[string][]string       // schema -> tables
	columns  map[string][]ColumnSchema // "schema.table" -> columns
	getCalls int64
}

func (f *fakeAPI) ListSchemas(_, _ string) ([]string, error) { return nil, nil }
func (f *fakeAPI) ListTables(_, _, schema string) ([]string, error) {
	return f.tables[schema], nil
}
func (f *fakeAPI) GetTable(_, _, schema, table string) ([]ColumnSchema, error) {
	atomic.AddInt64(&f.getCalls, 1)
	return f.columns[TableKey(schema, table)], nil
}

func TestFetcherFetch(t *testing.T) {
	api := &fakeAPI{
		tables: map[string][]string{"dbo": {"a", "b"}, "Dim": {"c"}},
		columns: map[string][]ColumnSchema{
			"dbo.a": {{Name: "id", Type: "string", Position: 0}},
			"dbo.b": {{Name: "x", Type: "long", Position: 0}},
			"Dim.c": {{Name: "k", Type: "int", Position: 0}},
		},
	}
	got, err := NewFetcher(api, 4).Fetch("ws", "lh", []string{"dbo", "Dim"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tables, want 3", len(got))
	}
	if ta, ok := got["dbo.a"]; !ok || ta.Schema != "dbo" || ta.Table != "a" || ta.Columns["id"].Type != "string" {
		t.Errorf("dbo.a = %+v, want schema dbo table a col id:string", ta)
	}
	if tc, ok := got["Dim.c"]; !ok || tc.Columns["k"].Type != "int" {
		t.Errorf("Dim.c = %+v, want col k:int", tc)
	}
	if api.getCalls != 3 {
		t.Errorf("expected 3 GetTable calls, got %d", api.getCalls)
	}
}

// countingAPI tracks peak concurrent GetTable calls and can inject a failure.
type countingAPI struct {
	tables      map[string][]string
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	getCalls    int
	listCalls   map[string]int
	failTable   string
}

func (c *countingAPI) ListSchemas(_, _ string) ([]string, error) { return nil, nil }
func (c *countingAPI) ListTables(_, _, schema string) ([]string, error) {
	c.mu.Lock()
	c.listCalls[schema]++
	c.mu.Unlock()
	return c.tables[schema], nil
}
func (c *countingAPI) GetTable(_, _, schema, table string) ([]ColumnSchema, error) {
	c.mu.Lock()
	c.inFlight++
	c.getCalls++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	c.mu.Unlock()
	time.Sleep(5 * time.Millisecond) // force overlap so the cap is observable
	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()
	if table == c.failTable {
		return nil, fmt.Errorf("boom on %s", table)
	}
	return []ColumnSchema{{Name: "c", Type: "string"}}, nil
}

func TestFetcherSharedBudgetBoundsConcurrency(t *testing.T) {
	tables := map[string][]string{"s": {"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9"}}
	api := &countingAPI{tables: tables, listCalls: map[string]int{}}
	f := NewFetcher(api, 4) // one shared budget of 4

	// Two concurrent Fetch calls (source + target) on the SAME fetcher must
	// share the budget — not stack to 8 concurrent GetTable.
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if _, err := f.Fetch("ws", "lh", []string{"s"}); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if api.maxInFlight > 4 {
		t.Errorf("peak concurrent GetTable = %d, want <= 4 (shared budget)", api.maxInFlight)
	}
	if api.maxInFlight < 2 {
		t.Errorf("peak concurrent GetTable = %d, expected real parallelism (>= 2)", api.maxInFlight)
	}
	if api.getCalls != 20 {
		t.Errorf("getCalls = %d, want 20 (10 tables x 2 fetches)", api.getCalls)
	}
}

func TestFetcherListsTablesConcurrentlyPerSchema(t *testing.T) {
	api := &countingAPI{
		tables:    map[string][]string{"a": {"t1"}, "b": {"t2"}, "c": {"t3"}},
		listCalls: map[string]int{},
	}
	got, err := NewFetcher(api, 4).Fetch("ws", "lh", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d tables, want 3", len(got))
	}
	for _, s := range []string{"a", "b", "c"} {
		if api.listCalls[s] != 1 {
			t.Errorf("ListTables(%s) called %d times, want exactly 1", s, api.listCalls[s])
		}
	}
}

func TestFetcherPropagatesError(t *testing.T) {
	api := &countingAPI{
		tables:    map[string][]string{"s": {"ok", "bad"}},
		listCalls: map[string]int{},
		failTable: "bad",
	}
	if _, err := NewFetcher(api, 2).Fetch("ws", "lh", []string{"s"}); err == nil {
		t.Fatal("expected the GetTable failure to propagate")
	}
}
