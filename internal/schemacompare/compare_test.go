package schemacompare

import "testing"

func ts(schema, table string, cols ...ColumnSchema) TableSchema {
	m := map[string]ColumnSchema{}
	for _, c := range cols {
		m[c.Name] = c
	}
	return TableSchema{Schema: schema, Table: table, Columns: m}
}

func TestCompareTableAddedRemovedAndMatching(t *testing.T) {
	src := map[string]TableSchema{
		"dbo.same": ts("dbo", "same", ColumnSchema{Name: "id", Type: "long"}),
		"dbo.only": ts("dbo", "only", ColumnSchema{Name: "x", Type: "string"}),
	}
	tgt := map[string]TableSchema{
		"dbo.same": ts("dbo", "same", ColumnSchema{Name: "id", Type: "long"}),
		"dbo.gone": ts("dbo", "gone", ColumnSchema{Name: "y", Type: "string"}),
	}
	tables, matching := Compare(src, tgt)
	if matching != 1 {
		t.Errorf("matching = %d, want 1 (dbo.same)", matching)
	}
	kinds := map[string]TableChangeKind{}
	for _, td := range tables {
		kinds[TableKey(td.Schema, td.Table)] = td.Kind
	}
	if kinds["dbo.only"] != TableNew {
		t.Errorf("dbo.only kind = %v, want TableNew", kinds["dbo.only"])
	}
	if kinds["dbo.gone"] != TableRemoved {
		t.Errorf("dbo.gone kind = %v, want TableRemoved", kinds["dbo.gone"])
	}
}

func TestCompareColumnChanges(t *testing.T) {
	src := map[string]TableSchema{
		"dbo.t": ts("dbo", "t",
			ColumnSchema{Name: "keep", Type: "long"},
			ColumnSchema{Name: "added", Type: "string"},
			ColumnSchema{Name: "retyped", Type: "bigint"},
		),
	}
	tgt := map[string]TableSchema{
		"dbo.t": ts("dbo", "t",
			ColumnSchema{Name: "keep", Type: "long"},
			ColumnSchema{Name: "removed", Type: "double"},
			ColumnSchema{Name: "retyped", Type: "int"},
		),
	}
	tables, matching := Compare(src, tgt)
	if matching != 0 {
		t.Errorf("matching = %d, want 0", matching)
	}
	if len(tables) != 1 || tables[0].Kind != TableChanged {
		t.Fatalf("want 1 changed table, got %+v", tables)
	}
	got := map[string]ColumnChange{}
	for _, cc := range tables[0].Columns {
		got[cc.Name] = cc
	}
	if got["added"].Kind != ColAdded || got["added"].NewType != "string" {
		t.Errorf("added = %+v", got["added"])
	}
	if got["removed"].Kind != ColRemoved || got["removed"].OldType != "double" {
		t.Errorf("removed = %+v", got["removed"])
	}
	if got["retyped"].Kind != ColTypeChanged || got["retyped"].OldType != "int" || got["retyped"].NewType != "bigint" {
		t.Errorf("retyped = %+v, want old int new bigint", got["retyped"])
	}
	if _, ok := got["keep"]; ok {
		t.Error("unchanged column 'keep' must not appear in the diff")
	}
}

func TestCompareEmpty(t *testing.T) {
	tables, matching := Compare(map[string]TableSchema{}, map[string]TableSchema{})
	if len(tables) != 0 || matching != 0 {
		t.Errorf("empty compare = %v / %d, want none/0", tables, matching)
	}
}
