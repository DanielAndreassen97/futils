package schemacompare

import "fmt"

// ColumnSchema is one column's name, formatted type, nullability and ordinal.
type ColumnSchema struct {
	Name     string
	Type     string
	Nullable bool
	Position int
}

// TableSchema is one table's columns keyed by column name.
type TableSchema struct {
	Schema  string
	Table   string
	Columns map[string]ColumnSchema
}

// TableKey is the "schema.table" key used in the per-side schema maps.
func TableKey(schema, table string) string {
	return schema + "." + table
}

// formatType renders a comparable type string: "decimal(p,s)" for decimals
// with precision, otherwise the raw type_name (string, long, double, date, …).
func formatType(typeName string, precision, scale int) string {
	if typeName == "decimal" && precision > 0 {
		return fmt.Sprintf("decimal(%d,%d)", precision, scale)
	}
	return typeName
}
