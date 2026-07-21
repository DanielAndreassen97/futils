// Package ui — banner.go draws the startup logo.
//
// The banner is a 7x8 pixel-font rendering of "FUTILS" with a two-pass
// colour gradient (light yellow-green → dark forest green) and a drop
// shadow. The design mirrors frefresh-go's banner for visual family
// resemblance — same pixel metrics, same shadow offset — but swaps the
// orange-to-yellow palette for green so the two tools are distinguishable
// at a glance when the user has both in muscle memory.
package ui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var dimStyle = lipgloss.NewStyle().
	Foreground(DimColor)

// Version is set by main at startup.
var Version = "dev"

// UpdateNotice, when non-empty, is a one-line upgrade hint main resolved at
// startup (e.g. "v0.9.0 available — brew upgrade futils"); the banner shows
// it centred under the version.
var UpdateNotice string

// DemoNotice, when non-empty, banners that every flow runs against the fake
// tenant — with the way back, which differs by how demo mode was entered
// (`futils demo`: just quit; FUTILS_DEMO export: unset it) — so demo mode can
// never masquerade as the real thing.
var DemoNotice string

// BuildInfo is an optional compact freshness line shown under the version in
// the banner — set by main for dev builds, empty (and hidden) for releases.
var BuildInfo string

// Each letter: 8 rows, 7 columns. A '1' pixel is filled; '0' is empty.
// Letters are hand-drawn at chunky 2-px stroke weight to match the
// aesthetic of the frefresh banner — thin strokes look anaemic against
// the drop shadow.
//
// CONVENTION: column 6 (the rightmost) should always be empty. It acts
// as implicit right-padding so the drop shadow can bleed into the gap
// without making the next letter feel stuck-on. Letters that break this
// rule (e.g. top bar "1111111" across full width) visually merge with
// their right neighbour. If you add a glyph, leave the last col 0.
var pixelFont = map[rune][8]string{
	'F': {
		"1111110",
		"1111110",
		"1100000",
		"1111100",
		"1111100",
		"1100000",
		"1100000",
		"1100000",
	},
	'U': {
		"1100110",
		"1100110",
		"1100110",
		"1100110",
		"1100110",
		"1100110",
		"1111110",
		"0111100",
	},
	'T': {
		// Top bar kept at "1111110" (cols 0-5) so col 6 stays empty as
		// right-padding — same convention as every other glyph.
		"1111110",
		"1111110",
		"0011100",
		"0011100",
		"0011100",
		"0011100",
		"0011100",
		"0011100",
	},
	'I': {
		// Narrow glyph centred on cols 1-4 (2-col stroke). Col 6 empty
		// like every other glyph. Serif-bars at top and bottom give it
		// weight against the chunky neighbours — a bare 2-col vertical
		// looks anaemic next to F/U/L.
		"1111110",
		"1111110",
		"0011000",
		"0011000",
		"0011000",
		"0011000",
		"1111110",
		"1111110",
	},
	'L': {
		"1100000",
		"1100000",
		"1100000",
		"1100000",
		"1100000",
		"1100000",
		"1111110",
		"1111110",
	},
	'S': {
		// Classic S: top bar curves down-left, middle bar reverses, bottom
		// bar curves down-right. 2-col verticals on the half-strokes keep
		// stroke weight consistent with F/L. The "0" at row 0 col 0 and
		// row 7 col 5 round the corners so the S doesn't look like a Z.
		"0111110",
		"1111110",
		"1100000",
		"1111100",
		"0111110",
		"0000110",
		"1111110",
		"1111100",
	},
}

const (
	letterW  = 7
	letterH  = 8
	spacing  = 1
	shadowDx = 1
	shadowDy = 1

	// Colour sweep parameters — adjust these to retune the gradient
	// without touching the rendering code. Sweep now goes LIGHT → DARK
	// left-to-right. Hue range deliberately narrow and centred on pure
	// green (110°–140°) so we avoid the "electric chartreuse" feel
	// that an earlier version had when it reached for 100°.
	hueStart    = 110.0 // bright pure green on the left
	hueEnd      = 140.0 // deep forest on the right
	lightStart  = 0.62  // bright, readable left edge
	lightEnd    = 0.32  // dark forest right edge
	shadowLight = 0.15  // darker shadow since main itself gets dark
	shadowSat   = 0.50  // crisp green shadow, not grey
	mainSat     = 0.90
)

// hslToRGB converts HSL (h in degrees 0-360, s/l in 0-1) to 8-bit RGB.
// Copied from frefresh — no point reimplementing a pure function.
func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - math.Abs(2*l-1)) * s
	hh := h / 60.0
	x := c * (1 - math.Abs(math.Mod(hh, 2)-1))
	var r, g, b float64
	switch {
	case hh < 1:
		r, g, b = c, x, 0
	case hh < 2:
		r, g, b = x, c, 0
	case hh < 3:
		r, g, b = 0, c, x
	case hh < 4:
		r, g, b = 0, x, c
	case hh < 5:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := l - c/2
	return int((r + m) * 255), int((g + m) * 255), int((b + m) * 255)
}

// lerpColour returns the main and shadow colours at normalised position t
// (0..1) along the gradient sweep.
func lerpColour(t float64) (main, shadow lipgloss.Color) {
	hue := hueStart + t*(hueEnd-hueStart)
	lightness := lightStart + t*(lightEnd-lightStart)

	r, g, b := hslToRGB(hue, mainSat, lightness)
	main = lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))

	sr, sg, sb := hslToRGB(hue, shadowSat, shadowLight)
	shadow = lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", sr, sg, sb))
	return
}

func buildGrid(word string) ([][]bool, int, int) {
	w := len(word)*(letterW+spacing) - spacing
	h := letterH
	grid := make([][]bool, h)
	for r := range grid {
		grid[r] = make([]bool, w)
	}
	for i, ch := range word {
		pix, ok := pixelFont[ch]
		if !ok {
			continue
		}
		xOff := i * (letterW + spacing)
		for row := 0; row < letterH; row++ {
			for col := 0; col < letterW && col < len(pix[row]); col++ {
				if pix[row][col] == '1' {
					grid[row][xOff+col] = true
				}
			}
		}
	}
	return grid, w, h
}

// gradientBanner renders the pixel-art word with a 1x1 drop shadow. Each
// terminal row covers two pixel rows (using half-block characters ▀ / ▄)
// so the banner prints in half the vertical space.
func gradientBanner() string {
	word := "FUTILS"
	grid, w, h := buildGrid(word)

	shadowW := w + shadowDx
	shadowH := h + shadowDy
	shadow := make([][]bool, shadowH)
	for r := range shadow {
		shadow[r] = make([]bool, shadowW)
	}
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			if grid[r][c] {
				sr, sc := r+shadowDy, c+shadowDx
				if sr < shadowH && sc < shadowW {
					shadow[sr][sc] = true
				}
			}
		}
	}

	fullGrid := make([][]bool, shadowH)
	for r := range fullGrid {
		fullGrid[r] = make([]bool, shadowW)
	}
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			fullGrid[r][c] = grid[r][c]
		}
	}

	totalW := shadowW

	var sb strings.Builder
	sb.WriteString(" ")
	outputRows := (shadowH + 1) / 2
	for pair := 0; pair < outputRows; pair++ {
		topIdx := pair * 2
		botIdx := pair*2 + 1
		for col := 0; col < totalW; col++ {
			topMain := topIdx < shadowH && fullGrid[topIdx][col]
			botMain := botIdx < shadowH && fullGrid[botIdx][col]
			topShad := topIdx < shadowH && shadow[topIdx][col] && !topMain
			botShad := botIdx < shadowH && shadow[botIdx][col] && !botMain

			t := float64(col) / float64(totalW-1)
			mainColor, shadColor := lerpColour(t)

			switch {
			case topMain && botMain:
				sb.WriteString(lipgloss.NewStyle().Foreground(mainColor).Render("█"))
			case topMain && botShad:
				sb.WriteString(lipgloss.NewStyle().Foreground(mainColor).Background(shadColor).Render("▀"))
			case topMain:
				sb.WriteString(lipgloss.NewStyle().Foreground(mainColor).Render("▀"))
			case topShad && botMain:
				sb.WriteString(lipgloss.NewStyle().Foreground(mainColor).Background(shadColor).Render("▄"))
			case botMain:
				sb.WriteString(lipgloss.NewStyle().Foreground(mainColor).Render("▄"))
			case topShad && botShad:
				sb.WriteString(lipgloss.NewStyle().Foreground(shadColor).Render("█"))
			case topShad:
				sb.WriteString(lipgloss.NewStyle().Foreground(shadColor).Render("▀"))
			case botShad:
				sb.WriteString(lipgloss.NewStyle().Foreground(shadColor).Render("▄"))
			default:
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n ")
	}
	return sb.String()
}

// gradientText colours inline text with the same sweep as the banner, so
// the version number and keymap hint carry the palette through.
func gradientText(text string) string {
	runes := []rune(text)
	var sb strings.Builder
	for i, ch := range runes {
		t := float64(i) / float64(max(len(runes)-1, 1))
		hue := hueStart + t*(hueEnd-hueStart)
		lightness := lightStart + t*(lightEnd-lightStart)
		r, g, b := hslToRGB(hue, mainSat, lightness)
		color := lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
		sb.WriteString(lipgloss.NewStyle().Foreground(color).Bold(true).Render(string(ch)))
	}
	return sb.String()
}

func centerPad(visibleLen, bannerWidth int) string {
	pad := (bannerWidth - visibleLen) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad)
}

// Banner returns the full startup banner: pixel-art logo, centred version
// string, and centred keymap hint. Intended to be printed once at launch.
func Banner() string {
	banner := gradientBanner()
	bannerWidth := len([]rune("FUTILS"))*(letterW+spacing) - spacing + shadowDx

	verText := "v" + Version
	ver := gradientText(verText)

	hintText := "↑↓ navigate • 1-9 select • enter confirm • esc back • q quit"
	hint := dimStyle.Render(hintText)

	out := "\n" + banner + "\n" +
		centerPad(len([]rune(verText)), bannerWidth) + ver
	if BuildInfo != "" {
		out += "\n" + centerPad(len([]rune(BuildInfo)), bannerWidth) + dimStyle.Render(BuildInfo)
	}
	if UpdateNotice != "" {
		notice := "⬆ " + UpdateNotice
		out += "\n" + centerPad(len([]rune(notice)), bannerWidth) +
			lipgloss.NewStyle().Foreground(AccentColor).Render(notice)
	}
	if DemoNotice != "" {
		out += "\n" + centerPad(len([]rune(DemoNotice)), bannerWidth) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Bold(true).Render(DemoNotice)
	}
	out += "\n" + centerPad(len([]rune(hintText)), bannerWidth) + hint
	return out
}
