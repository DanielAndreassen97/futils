package cmd

import (
	"fmt"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/schemacompare"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// Terminal styles for the schema-compare result. ANSI-palette colours (1/2/3)
// so they track the user's terminal theme, matching the deploy compare output.
var (
	scTitleStyle = lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true)
	scDimStyle   = lipgloss.NewStyle().Foreground(ui.DimColor)
	scLhStyle    = lipgloss.NewStyle().Foreground(ui.AccentColor).Bold(true)
	scTblStyle   = lipgloss.NewStyle().Bold(true)
	scAddStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	scRemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	scChgStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	scOkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

// renderSchemaCompareTerminal renders the per-lakehouse, per-table diff with
// colour: "+" (green) only in source, "-" (red) only in target, "~" (yellow)
// type changed. Column names are padded so types line up within a table.
func renderSchemaCompareTerminal(srcLabel, tgtLabel string, diffs []schemacompare.LakehouseDiff) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n  %s  %s %s %s\n",
		scTitleStyle.Render("Schema compare"),
		scDimStyle.Render(srcLabel), scDimStyle.Render("→"), scDimStyle.Render(tgtLabel))
	fmt.Fprintf(&b, "  %s\n", scDimStyle.Render(fmt.Sprintf(
		"+ only in %s    - only in %s    ~ type changed", srcLabel, tgtLabel)))

	for _, lh := range diffs {
		schemas := scDimStyle.Render(strings.Join(lh.Schemas, ", "))
		fmt.Fprintf(&b, "\n  %s  %s\n", scLhStyle.Render(lh.Lakehouse), schemas)

		if len(lh.Tables) == 0 {
			fmt.Fprintf(&b, "    %s\n",
				scOkStyle.Render(fmt.Sprintf("✓ no differences (%d matching tables)", lh.Matching)))
			continue
		}

		for _, td := range lh.Tables {
			name := schemacompare.TableKey(td.Schema, td.Table)
			switch td.Kind {
			case schemacompare.TableNew:
				fmt.Fprintf(&b, "    %s %s    %s\n",
					scAddStyle.Render("+"), scTblStyle.Render(name), scDimStyle.Render("new table"))
			case schemacompare.TableRemoved:
				fmt.Fprintf(&b, "    %s %s    %s\n",
					scRemStyle.Render("-"), scTblStyle.Render(name), scDimStyle.Render("removed"))
			case schemacompare.TableChanged:
				fmt.Fprintf(&b, "    %s\n", scTblStyle.Render(name))
				pad := 0
				for _, cc := range td.Columns {
					if len(cc.Name) > pad {
						pad = len(cc.Name)
					}
				}
				for _, cc := range td.Columns {
					col := fmt.Sprintf("%-*s", pad, cc.Name)
					switch cc.Kind {
					case schemacompare.ColAdded:
						fmt.Fprintf(&b, "      %s %s  %s\n",
							scAddStyle.Render("+"), col, scAddStyle.Render(cc.NewType))
					case schemacompare.ColRemoved:
						fmt.Fprintf(&b, "      %s %s  %s\n",
							scRemStyle.Render("-"), col, scRemStyle.Render(cc.OldType))
					case schemacompare.ColTypeChanged:
						fmt.Fprintf(&b, "      %s %s  %s\n",
							scChgStyle.Render("~"), col, scChgStyle.Render(cc.OldType+" → "+cc.NewType))
					}
				}
			}
		}
		fmt.Fprintf(&b, "    %s\n", scDimStyle.Render(fmt.Sprintf("%d unchanged", lh.Matching)))
	}
	return b.String()
}
