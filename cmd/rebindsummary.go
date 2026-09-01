package cmd

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/DanielAndreassen97/futils/internal/deploy"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

var (
	summaryDimStyle   = lipgloss.NewStyle().Foreground(ui.DimColor)
	summaryArrowStyle = lipgloss.NewStyle().Foreground(ui.AccentColor)
	summaryNameStyle  = lipgloss.NewStyle().Bold(true)
)

// compactValue shortens a long identifier for the one-line rebind summary:
// first 8 characters, an ellipsis, last 4 — enough to tell two GUIDs apart
// and to eyeball a match against the Fabric UI, while keeping a full
// baseline → target pair inside 80 columns. Values of 16 characters or fewer
// (human names, short fixtures) are returned unchanged. The full values are
// always in the HTML diff report.
func compactValue(v string) string {
	const head, tail = 8, 4
	r := []rune(v)
	if len(r) <= head+tail+4 {
		return v
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// hostSuffix returns everything from the first "." of a hostname-shaped value
// (".datawarehouse.fabric.microsoft.com"), or "" for values without a dot.
func hostSuffix(v string) string {
	if i := strings.Index(v, "."); i >= 0 {
		return v[i:]
	}
	return ""
}

// sharedHostSuffix returns the domain every host-shaped change in the group
// has on BOTH sides of its rewrite, so the rows can show only the part that
// differs and the header can carry the domain once. Rows without a dot
// (GUIDs) neither contribute nor veto; a single host whose sides disagree, or
// two hosts on different domains, disables the trim for the whole group.
func sharedHostSuffix(changes []deploy.RebindChange) string {
	shared := ""
	for _, c := range changes {
		o, n := hostSuffix(c.Old), hostSuffix(c.New)
		if o == "" && n == "" {
			continue
		}
		if o != n || (shared != "" && shared != o) {
			return ""
		}
		shared = o
	}
	return shared
}

// kindStyle colors a rebind Kind header with the item type's color from the
// deploy overview, so "Lakehouse" is the same blue in both places. Kinds that
// aren't item types (Workspace) or have no dedicated color fall back to bold.
func kindStyle(kind string) lipgloss.Style {
	typ := kind
	if kind == "SQL endpoint" {
		typ = "SQLEndpoint"
	}
	if c := ui.ItemTypeColor(typ); c != ui.DimColor {
		return lipgloss.NewStyle().Bold(true).Foreground(c)
	}
	return lipgloss.NewStyle().Bold(true)
}

// padRight pads s with spaces to w runes (not bytes — the ellipsis in a
// compacted value is multi-byte, and %-*s would misalign the column).
func padRight(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// printAutoRebinds renders the auto-recognized rewrites grouped by Kind (the
// input is already sorted by Kind, Name), one line per reference:
//
//	Lakehouse (2)
//	  LH_Bronze     a1b2c3d4…6666  →  f0e1d2c3…6666
//	  LH_ConfigLog  b2c3d4e5…6666  →  e1f2a3b4…6666
//
// A name repeated within a group (an endpoint rebinding both host and id)
// prints once; the header carries the count and, for hosts, the shared domain.
func printAutoRebinds(auto []deploy.RebindChange) {
	for start := 0; start < len(auto); {
		end := start
		for end < len(auto) && auto[end].Kind == auto[start].Kind {
			end++
		}
		printRebindGroup(auto[start:end])
		start = end
	}
}

func printRebindGroup(changes []deploy.RebindChange) {
	kind := changes[0].Kind
	suffix := sharedHostSuffix(changes)
	header := "  " + kindStyle(kind).Render(kind) + " " + summaryDimStyle.Render(fmt.Sprintf("(%d)", len(changes)))
	if suffix != "" {
		header += summaryDimStyle.Render(" · *" + suffix)
	}
	fmt.Println()
	fmt.Println(header)

	type row struct{ name, old, new string }
	rows := make([]row, 0, len(changes))
	nameW, oldW := 0, 0
	for _, c := range changes {
		o, n := c.Old, c.New
		if suffix != "" && strings.HasSuffix(o, suffix) && strings.HasSuffix(n, suffix) {
			o, n = strings.TrimSuffix(o, suffix), strings.TrimSuffix(n, suffix)
		}
		r := row{name: c.Name, old: compactValue(o), new: compactValue(n)}
		rows = append(rows, r)
		nameW = max(nameW, utf8.RuneCountInString(r.name))
		oldW = max(oldW, utf8.RuneCountInString(r.old))
	}
	last := ""
	for _, r := range rows {
		name := r.name
		if name == last {
			name = "" // same reference, second value (e.g. endpoint host + id)
		} else {
			last = r.name
		}
		fmt.Printf("    %s  %s  %s  %s\n",
			summaryNameStyle.Render(padRight(name, nameW)),
			summaryDimStyle.Render(padRight(r.old, oldW)),
			summaryArrowStyle.Render("→"),
			r.new)
	}
}
