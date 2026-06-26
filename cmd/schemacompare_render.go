package cmd

import (
	"fmt"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
)

// renderSchemaCompareTerminal renders the per-lakehouse, per-table diff as
// plain text. source = "+" side, target = "-" side, "~" = type changed.
func renderSchemaCompareTerminal(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schema compare: %s → %s\n", srcLabel, tgtLabel)
	fmt.Fprintf(&b, "  \"+\" = only in %s   \"-\" = only in %s   \"~\" = type changed\n", srcLabel, tgtLabel)

	for _, lh := range diffs {
		fmt.Fprintf(&b, "\n  %s [%s]:\n", lh.Lakehouse, strings.Join(lh.Schemas, ", "))
		if len(lh.Tables) == 0 {
			fmt.Fprintf(&b, "    = %d matching (no differences)\n", lh.Matching)
			continue
		}
		for _, td := range lh.Tables {
			name := schemacompare.TableKey(td.Schema, td.Table)
			switch td.Kind {
			case schemacompare.TableNew:
				fmt.Fprintf(&b, "    + NEW TABLE  %s\n", name)
			case schemacompare.TableRemoved:
				fmt.Fprintf(&b, "    - REMOVED    %s\n", name)
			case schemacompare.TableChanged:
				fmt.Fprintf(&b, "    %s\n", name)
				for _, cc := range td.Columns {
					switch cc.Kind {
					case schemacompare.ColAdded:
						fmt.Fprintf(&b, "      + %s:  %s\n", cc.Name, cc.NewType)
					case schemacompare.ColRemoved:
						fmt.Fprintf(&b, "      - %s:  %s\n", cc.Name, cc.OldType)
					case schemacompare.ColTypeChanged:
						fmt.Fprintf(&b, "      ~ %s:  %s → %s\n", cc.Name, cc.OldType, cc.NewType)
					}
				}
			}
		}
		fmt.Fprintf(&b, "\n    = %d matching\n", lh.Matching)
	}
	return b.String()
}
