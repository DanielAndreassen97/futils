package ui

import "github.com/charmbracelet/lipgloss"

// AccentColor is the brand green used for cursors, highlights and focus
// states throughout the UI. This is the futils-specific palette —
// frefresh-go uses orange (#e8712a); futils uses green (Tailwind
// green-500) to keep the two tools visually distinct when muscle memory
// would otherwise confuse them.
var AccentColor = lipgloss.Color("#22c55e")

// DimColor is the muted gray used for secondary text (hints, labels,
// deselected rows). Terminal-theme-agnostic — ANSI 8 reads as grey on
// both light and dark backgrounds.
var DimColor = lipgloss.Color("8")

// WarnColor is the yellow used for "pay attention" tags like inline
// badges. ANSI 3 stays within the terminal-adaptive 0-7 range, same
// rationale as DimColor, so it reads as yellow on both light and dark
// backgrounds instead of a fixed hex that could clash with either.
var WarnColor = lipgloss.Color("3")

// StopColor is the red used for "this did not happen" lines — a
// cancelled action, an aborted delete. Reserved for outcomes the user
// must not misread as success, since those lines scroll past in the
// same wall of output as the ones that did go through. ANSI 1 for the
// same terminal-adaptive reason as DimColor and WarnColor.
var StopColor = lipgloss.Color("1")

// ItemTypeStyle is how one Fabric item type is coloured. Text and Bar are the
// same hue rendered two ways: several of Microsoft's item colours — the
// lakehouse blue and the report gold especially — are too dark to read as
// foreground text on a dark terminal, so Text is a lightened variant of the
// hue that Bar uses as a background.
type ItemTypeStyle struct {
	Text  lipgloss.Color
	BarBG lipgloss.Color
	BarFG lipgloss.Color
}

// itemTypeStyles is the per-type colouring, taken from Microsoft's published
// Fabric icon set rather than invented, so a futils list reads the way the
// portal does: lakehouse blue, notebook and pipeline green, semantic model
// purple, report gold.
//
// One table, because there used to be two that disagreed — the move picker
// coloured a report orange while the item browser coloured it gold, and the same
// item looked like two different things depending on the screen.
var itemTypeStyles = map[string]ItemTypeStyle{
	// Data Engineering and Data Factory — green (#45913e).
	"Notebook":           {"#5cb054", "#45913e", "#f0fdf4"},
	"DataPipeline":       {"#5cb054", "#45913e", "#f0fdf4"},
	"Environment":        {"#5cb054", "#45913e", "#f0fdf4"},
	"Dataflow":           {"#5cb054", "#45913e", "#f0fdf4"},
	"SparkJobDefinition": {"#5cb054", "#45913e", "#f0fdf4"},
	"MLModel":            {"#5cb054", "#45913e", "#f0fdf4"},
	"MLExperiment":       {"#5cb054", "#45913e", "#f0fdf4"},
	"VariableLibrary":    {"#5cb054", "#45913e", "#f0fdf4"},
	"CopyJob":            {"#5cb054", "#45913e", "#f0fdf4"},
	// Lakehouse and Warehouse — blue (#2661be) and cyan (#20b6ef).
	"Lakehouse": {"#5b8fe0", "#2661be", "#eef4fd"},
	"Warehouse": {"#20b6ef", "#20b6ef", "#06222c"},
	// Databases and Real-Time Intelligence — blue (#007fca).
	"SQLDatabase":  {"#3aa9e8", "#007fca", "#eaf6ff"},
	"SQLEndpoint":  {"#3aa9e8", "#007fca", "#eaf6ff"},
	"Eventhouse":   {"#3aa9e8", "#007fca", "#eaf6ff"},
	"Eventstream":  {"#3aa9e8", "#007fca", "#eaf6ff"},
	"KQLDatabase":  {"#3aa9e8", "#007fca", "#eaf6ff"},
	"KQLQueryset":  {"#3aa9e8", "#007fca", "#eaf6ff"},
	"KQLDashboard": {"#3aa9e8", "#007fca", "#eaf6ff"},
	// Power BI — purple (#744fb5) for models, gold (#bc7d00) for reports.
	"SemanticModel":   {"#a98ae0", "#744fb5", "#f6f0fc"},
	"Report":          {"#d9a520", "#bc7d00", "#1c1300"},
	"PaginatedReport": {"#d9a520", "#bc7d00", "#1c1300"},
	"Dashboard":       {"#d9a520", "#bc7d00", "#1c1300"},
}

// itemTypeFallbackStyle covers a type Microsoft ships that futils has not been
// told about — grey rather than unstyled, so a new type still renders as a type.
var itemTypeFallbackStyle = ItemTypeStyle{DimColor, "#3a4547", "#d7dfe0"}

// ItemTypeStyleFor returns the colouring for a Fabric item type.
func ItemTypeStyleFor(itemType string) ItemTypeStyle {
	if s, ok := itemTypeStyles[itemType]; ok {
		return s
	}
	return itemTypeFallbackStyle
}

// ItemTypeColor is the colour for a Fabric item type rendered as text.
func ItemTypeColor(itemType string) lipgloss.Color {
	return ItemTypeStyleFor(itemType).Text
}
