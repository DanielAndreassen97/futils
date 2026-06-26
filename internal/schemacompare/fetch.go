package schemacompare

import "sync"

type fetchJob struct {
	schema string
	table  string
}

// Fetcher loads table/column schemas from the OneLake Table API. A single
// Fetcher carries ONE shared concurrency budget, so concurrent Fetch calls
// (e.g. the source and target lakehouse sides running at once) stay within one
// total in-flight limit rather than each opening its own pool and stacking the
// load on OneLake. Within a Fetch, ListTables runs concurrently across schemas
// and GetTable runs concurrently across tables — both drawing from that budget.
type Fetcher struct {
	api OneLakeTableAPI
	sem chan struct{}
}

// NewFetcher returns a Fetcher whose total in-flight API calls are capped at
// concurrency (minimum 1).
func NewFetcher(api OneLakeTableAPI, concurrency int) *Fetcher {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Fetcher{api: api, sem: make(chan struct{}, concurrency)}
}

func (f *Fetcher) acquire() { f.sem <- struct{}{} }
func (f *Fetcher) release() { <-f.sem }

// Fetch returns every table's column schema for the given schemas of one
// lakehouse, keyed by TableKey. Returns the first error encountered.
func (f *Fetcher) Fetch(wsID, lhID string, schemas []string) (map[string]TableSchema, error) {
	// Phase 1: list tables for every schema concurrently. Avoids the serial
	// per-schema latency that dominated when a lakehouse had many schemas.
	type listed struct {
		schema string
		tables []string
		err    error
	}
	listResults := make([]listed, len(schemas))
	var lwg sync.WaitGroup
	for i, schema := range schemas {
		lwg.Add(1)
		f.acquire()
		go func(i int, schema string) {
			defer lwg.Done()
			defer f.release()
			tables, err := f.api.ListTables(wsID, lhID, schema)
			listResults[i] = listed{schema: schema, tables: tables, err: err}
		}(i, schema)
	}
	lwg.Wait()

	var jobs []fetchJob
	for _, lr := range listResults {
		if lr.err != nil {
			return nil, lr.err
		}
		for _, table := range lr.tables {
			jobs = append(jobs, fetchJob{lr.schema, table})
		}
	}

	// Phase 2: fetch each table's columns concurrently. GetTable-per-table is
	// the cost driver; the only batch the API offers is a table at a time.
	result := make(map[string]TableSchema, len(jobs))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	for _, j := range jobs {
		wg.Add(1)
		f.acquire()
		go func(j fetchJob) {
			defer wg.Done()
			defer f.release()
			cols, err := f.api.GetTable(wsID, lhID, j.schema, j.table)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			byName := make(map[string]ColumnSchema, len(cols))
			for _, c := range cols {
				byName[c.Name] = c
			}
			result[TableKey(j.schema, j.table)] = TableSchema{Schema: j.schema, Table: j.table, Columns: byName}
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return result, nil
}
