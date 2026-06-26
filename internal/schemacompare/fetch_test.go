package schemacompare

import (
	"sync/atomic"
	"testing"
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

func TestFetchSchema(t *testing.T) {
	api := &fakeAPI{
		tables: map[string][]string{"dbo": {"a", "b"}},
		columns: map[string][]ColumnSchema{
			"dbo.a": {{Name: "id", Type: "string", Position: 0}},
			"dbo.b": {{Name: "x", Type: "long", Position: 0}},
		},
	}
	got, err := FetchSchema(api, "ws", "lh", []string{"dbo"}, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tables, want 2", len(got))
	}
	ta, ok := got["dbo.a"]
	if !ok || ta.Schema != "dbo" || ta.Table != "a" || ta.Columns["id"].Type != "string" {
		t.Errorf("dbo.a = %+v, want schema dbo table a col id:string", ta)
	}
	if api.getCalls != 2 {
		t.Errorf("expected 2 GetTable calls, got %d", api.getCalls)
	}
}
