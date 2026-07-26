package cmd

import (
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/charmbracelet/lipgloss"
)

// tierColW is the width of the licence-tier column. Every tier name fits, so
// the SKUs beside them line up down the whole list.
const tierColW = 8

// Tier colours. Fabric, Pro and Personal reuse the existing palette; the three
// Power BI capacity families need their own hues and are hex because the
// terminal-adaptive ANSI 0-7 range has nothing left that reads distinctly.
var (
	tierFabricColor   = ui.AccentColor
	tierProColor      = ui.WarnColor
	tierPersonalColor = ui.DimColor
	tierPPUColor      = lipgloss.Color("#e8843a") // orange
	tierPremiumColor  = lipgloss.Color("#a978e8") // purple
	tierEmbeddedColor = lipgloss.Color("#3fb6c4") // cyan
	// tierUnknownColor marks a capacity futils could not resolve. Muted red
	// rather than dim: it is worth noticing that the answer is incomplete.
	tierUnknownColor = lipgloss.Color("#b05c5c")
)

// workspaceTier is a workspace's licence tier, ready to render. SKU is empty
// when the tier has no capacity behind it (Pro, Personal) or when the capacity
// could not be resolved.
type workspaceTier struct {
	Name  string
	SKU   string
	Color lipgloss.Color
}

// classifyWorkspace works out which licence a workspace runs on, from data
// futils already has: the workspace's type and capacity ID, plus the capacity
// list. No extra API call.
//
// The old UI said "no capacity", which described futils' knowledge rather than
// the workspace — every workspace has a licence tier, and a workspace without a
// capacity is precisely what everyone calls a Pro workspace.
//
// SKU families per Microsoft's capacity documentation: F2-F8192 are Fabric,
// P1-P5 are Premium, EM1-EM3 and A1-A8 are Embedded. PP3 is NOT documented,
// but Premium Per User workspaces are backed by an internal PP capacity and
// that is the SKU the capacities API returns for them. Matching on the prefix
// keeps an undocumented family from being reported as something it isn't;
// anything unrecognised degrades to "there is a capacity here", never to a
// wrong tier name.
func classifyWorkspace(ws fabric.Workspace, caps []fabric.Capacity) workspaceTier {
	// A capacity admin's My workspace is assigned to a capacity, but "Personal"
	// is still the more useful thing to say about it.
	if ws.Type == "Personal" {
		return workspaceTier{Name: "Personal", Color: tierPersonalColor}
	}
	if ws.CapacityID == "" {
		return workspaceTier{Name: "Pro", Color: tierProColor}
	}

	sku := ""
	for _, c := range caps {
		if c.ID == ws.CapacityID {
			sku = strings.ToUpper(c.SKU)
			break
		}
	}
	if sku == "" {
		// There is a capacity; we just cannot see it. Reporting Pro here would
		// be an outright lie about how the workspace is licensed.
		return workspaceTier{Name: "Capacity", Color: tierUnknownColor}
	}

	switch {
	// PP before P: a Premium Per User SKU starts with the same letter as
	// Premium, so the broader test has to come second.
	case strings.HasPrefix(sku, "PP"):
		return workspaceTier{Name: "PPU", SKU: sku, Color: tierPPUColor}
	case strings.HasPrefix(sku, "F"):
		return workspaceTier{Name: "Fabric", SKU: sku, Color: tierFabricColor}
	case strings.HasPrefix(sku, "P"):
		return workspaceTier{Name: "Premium", SKU: sku, Color: tierPremiumColor}
	case strings.HasPrefix(sku, "EM"), strings.HasPrefix(sku, "A"):
		return workspaceTier{Name: "Embedded", SKU: sku, Color: tierEmbeddedColor}
	}
	return workspaceTier{Name: "Capacity", SKU: sku, Color: tierUnknownColor}
}

// render draws the tier column: the tier name in its colour, then the SKU
// dimmed beside it. The name is padded before styling — FitWidth counts runes,
// and ANSI escapes would inflate the count.
func (t workspaceTier) render() string {
	out := lipgloss.NewStyle().Foreground(t.Color).Render(ui.FitWidth(t.Name, tierColW))
	if t.SKU != "" {
		out += " " + wsLabelStyle.Render(t.SKU)
	}
	return out
}

// plain is the unstyled form, used for the selected row, which renders in one
// uniform highlight. Padded to the same width as render so the two forms are
// interchangeable — an unpadded variant looks identical today, with no
// background behind it, and would silently ragged the column the moment one
// came back.
func (t workspaceTier) plain() string {
	out := ui.FitWidth(t.Name, tierColW)
	if t.SKU != "" {
		out += " " + t.SKU
	}
	return out
}
