package cmd

import (
	"fmt"
	"regexp"
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

	type row struct{ name, field, old, new, where string }
	rows := make([]row, 0, len(changes))
	nameW, fieldW, oldW, newW := 0, 0, 0, 0
	for _, c := range changes {
		o, n := c.Old, c.New
		if suffix != "" && strings.HasSuffix(o, suffix) && strings.HasSuffix(n, suffix) {
			o, n = strings.TrimSuffix(o, suffix), strings.TrimSuffix(n, suffix)
		}
		r := row{name: changeName(c), field: endpointField(c), old: compactValue(displayOld(o)), new: compactValue(n)}
		r.where = whereUsed(c.Items)
		rows = append(rows, r)
		nameW = max(nameW, utf8.RuneCountInString(r.name))
		fieldW = max(fieldW, utf8.RuneCountInString(r.field))
		oldW = max(oldW, utf8.RuneCountInString(r.old))
		newW = max(newW, utf8.RuneCountInString(r.new))
	}
	last := ""
	for _, r := range rows {
		name := r.name
		if name == last {
			name = "" // same reference, second value (e.g. endpoint host + id)
		} else {
			last = r.name
		}
		line := "    " + summaryNameStyle.Render(padRight(name, nameW))
		if fieldW > 0 {
			line += "  " + summaryDimStyle.Render(padRight(r.field, fieldW))
		}
		line += "  " + summaryDimStyle.Render(padRight(r.old, oldW)) + "  " + summaryArrowStyle.Render("→") + "  "
		if r.where != "" {
			line += padRight(r.new, newW) + "  " + summaryDimStyle.Render(r.where)
		} else {
			line += r.new
		}
		fmt.Println(line)
	}
}

// endpointField names which half of an SQL endpoint a change rewrites:
// Sql.Database(host, database) carries the server host and, in place of a
// database name, often the endpoint's id — two anonymous rows otherwise.
func endpointField(c deploy.RebindChange) string {
	if c.Kind != "SQL endpoint" {
		return ""
	}
	if hostSuffix(c.Old) != "" {
		return "host"
	}
	return "endpoint id"
}

// whereUsed renders which items carry a reference, always within the row:
// up to two by name, more as a count. A workspace reference can sit in every
// notebook of a mapping, and listing those under the row broke the one line
// per reference the summary is read by; the diff report names them all.
func whereUsed(items []deploy.ItemRef) string {
	switch {
	case len(items) == 0:
		return ""
	case len(items) <= 2:
		labels := make([]string, len(items))
		for i, it := range items {
			labels[i] = itemLabel(it)
		}
		return "in " + strings.Join(labels, ", ")
	default:
		return fmt.Sprintf("in %d items", len(items))
	}
}

// typePrefix matches the naming convention that already carries an item's
// type. A whitelist, not any capitals: HR_ or ERP_ name a business area, and
// "HR_Permissions" beside NB_ rows must still say it is a semantic model.
var typePrefix = regexp.MustCompile(`^(NB|PL|LH|SM|DF|WH|RPT|MIR|VL|ENV|SJD|KQL|EH|DS)_`)

// changeName is a rebind row's name, with the target's type when the change
// carries one (a shortcut can point at any OneLake item).
func changeName(c deploy.RebindChange) string {
	return itemLabel(deploy.ItemRef{Name: c.Name, Type: c.TargetType})
}

// displayOld is a rebind row's baseline value as shown to the user: the
// all-zeros workspace GUID is Fabric's "this workspace" placeholder, not a
// real workspace.
func displayOld(v string) string {
	if v == deploy.ThisWorkspaceGUID {
		return "(this workspace)"
	}
	return v
}

// itemLabel names an item for the summary, adding its type when the name
// doesn't already imply it — "Permissions" beside NB_ rows reads as a
// notebook otherwise.
func itemLabel(it deploy.ItemRef) string {
	if it.Type == "" || typePrefix.MatchString(it.Name) {
		return it.Name
	}
	return it.Name + " (" + it.Type + ")"
}
