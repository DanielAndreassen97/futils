package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// windowedList writes a cursor-centred viewport over total rows into b. When
// the terminal height can't fit them all, a slice around the cursor renders,
// framed by "↑ N more above" / "↓ N more below" hints (two reserved lines) so
// the frame never clips at the viewport edge; otherwise every row renders.
// headerRows counts the lines already printed above the list, hintStyle
// styles the frame hints, and renderRow renders the row at list position pos.
// Single home for the windowing math shared by the multi-selects, the filter
// menu and the refresh table picker.
func windowedList(b *strings.Builder, total, cursor, termHeight, headerRows int, hintStyle lipgloss.Style, renderRow func(pos int) string) {
	maxVisible := termHeight - headerRows - 1
	if maxVisible <= 0 || maxVisible >= total {
		for i := 0; i < total; i++ {
			fmt.Fprintf(b, "%s\n", renderRow(i))
		}
		return
	}

	itemSlots := maxVisible - 2
	if itemSlots < 1 {
		itemSlots = 1
	}
	start := cursor - itemSlots/2
	if start < 0 {
		start = 0
	}
	end := start + itemSlots
	if end > total {
		end = total
		start = end - itemSlots
		if start < 0 {
			start = 0
		}
	}

	if start > 0 {
		fmt.Fprintf(b, "  %s\n", hintStyle.Render(fmt.Sprintf("↑ %d more above", start)))
	}
	for i := start; i < end; i++ {
		fmt.Fprintf(b, "%s\n", renderRow(i))
	}
	if end < total {
		fmt.Fprintf(b, "  %s\n", hintStyle.Render(fmt.Sprintf("↓ %d more below", total-end)))
	}
}
