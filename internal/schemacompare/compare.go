package schemacompare

import "sort"

type ColumnChangeKind int

const (
	ColAdded ColumnChangeKind = iota
	ColRemoved
	ColTypeChanged
)

type ColumnChange struct {
	Name    string
	Kind    ColumnChangeKind
	OldType string // target side
	NewType string // source side
}

type TableChangeKind int

const (
	TableNew     TableChangeKind = iota // only in source
	TableRemoved                        // only in target
	TableChanged                        // exists both, columns differ
)

type TableDiff struct {
	Schema  string
	Table   string
	Kind    TableChangeKind
	Columns []ColumnChange // populated when Kind == TableChanged
}

// LakehouseDiff is one lakehouse's full comparison result. The caller fills
// Lakehouse and Schemas; Compare produces Tables and Matching.
type LakehouseDiff struct {
	Lakehouse string
	Schemas   []string
	Tables    []TableDiff
	Matching  int
}

// Compare diffs two side maps keyed by TableKey. source is the promotion
// origin (+), target the destination (-). Returns differing tables (sorted by
// key) and the count of fully-matching tables.
func Compare(source, target map[string]TableSchema) (tables []TableDiff, matching int) {
	keys := map[string]bool{}
	for k := range source {
		keys[k] = true
	}
	for k := range target {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	for _, k := range ordered {
		s, inS := source[k]
		tt, inT := target[k]
		switch {
		case inS && !inT:
			tables = append(tables, TableDiff{Schema: s.Schema, Table: s.Table, Kind: TableNew})
		case !inS && inT:
			tables = append(tables, TableDiff{Schema: tt.Schema, Table: tt.Table, Kind: TableRemoved})
		default:
			changes := compareColumns(s.Columns, tt.Columns)
			if len(changes) == 0 {
				matching++
				continue
			}
			tables = append(tables, TableDiff{Schema: s.Schema, Table: s.Table, Kind: TableChanged, Columns: changes})
		}
	}
	return tables, matching
}

func compareColumns(source, target map[string]ColumnSchema) []ColumnChange {
	names := map[string]bool{}
	for n := range source {
		names[n] = true
	}
	for n := range target {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	var changes []ColumnChange
	for _, n := range ordered {
		sc, inS := source[n]
		tc, inT := target[n]
		switch {
		case inS && !inT:
			changes = append(changes, ColumnChange{Name: n, Kind: ColAdded, NewType: sc.Type})
		case !inS && inT:
			changes = append(changes, ColumnChange{Name: n, Kind: ColRemoved, OldType: tc.Type})
		case sc.Type != tc.Type:
			changes = append(changes, ColumnChange{Name: n, Kind: ColTypeChanged, OldType: tc.Type, NewType: sc.Type})
		}
	}
	return changes
}
