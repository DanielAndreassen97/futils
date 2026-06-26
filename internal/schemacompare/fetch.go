package schemacompare

import "sync"

type fetchJob struct {
	schema string
	table  string
}

// FetchSchema returns every table's column schema for the given schemas,
// keyed by TableKey. GetTable runs through a bounded worker pool; the first
// error is returned (other workers finish but their results are discarded).
func FetchSchema(api OneLakeTableAPI, wsID, lhID string, schemas []string, concurrency int) (map[string]TableSchema, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	var jobs []fetchJob
	for _, schema := range schemas {
		tables, err := api.ListTables(wsID, lhID, schema)
		if err != nil {
			return nil, err
		}
		for _, table := range tables {
			jobs = append(jobs, fetchJob{schema, table})
		}
	}

	result := make(map[string]TableSchema, len(jobs))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, concurrency)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j fetchJob) {
			defer wg.Done()
			defer func() { <-sem }()
			cols, err := api.GetTable(wsID, lhID, j.schema, j.table)
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
