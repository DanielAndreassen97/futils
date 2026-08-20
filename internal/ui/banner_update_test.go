package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The update badge must carry both halves of the message: the version that is
// available, and the command that installs it. The old single-line notice put
// them in one green string that blended into the banner; if either half goes
// missing from the render, the badge is useless.
func TestBannerUpdateBadgeShowsVersionAndCommand(t *testing.T) {
	t.Cleanup(func() { UpdateVersion, Version = "", "dev" })
	Version = "0.10.0"
	UpdateVersion = "v0.10.1"

	out := Banner()
	if !strings.Contains(out, "⬆ UPDATE") {
		t.Errorf("badge label missing from banner:\n%s", out)
	}
	if !strings.Contains(out, "v0.10.1 available") {
		t.Errorf("available version missing from banner:\n%s", out)
	}
	if !strings.Contains(out, upgradeHint) {
		t.Errorf("upgrade command missing from banner:\n%s", out)
	}
}

// No newer release means no badge and no stray blank lines.
func TestBannerNoUpdateNoBadge(t *testing.T) {
	t.Cleanup(func() { UpdateVersion, Version = "", "dev" })
	Version = "0.10.1"
	UpdateVersion = ""

	out := Banner()
	if strings.Contains(out, "UPDATE") || strings.Contains(out, upgradeHint) {
		t.Errorf("badge rendered with no update available:\n%s", out)
	}
}

// The badge line must be centred against the banner width, and the command
// must sit on its own line under it. A regression here (padding dropped, or
// computed against the wrong width) puts the badge hard against the left edge
// while every test above still passes.
func TestBannerUpdateBadgeIsCentredUnderTheVersion(t *testing.T) {
	t.Cleanup(func() { UpdateVersion, Version = "", "dev" })
	Version = "0.10.0"
	UpdateVersion = "v0.10.1"

	const bannerWidth = 48 // 6 letters * (7+1) - 1 spacing + 1 shadow
	var badgeLine, cmdLine string
	for _, line := range strings.Split(Banner(), "\n") {
		switch {
		case strings.Contains(line, "⬆ UPDATE"):
			badgeLine = line
		case strings.Contains(line, upgradeHint):
			cmdLine = line
		}
	}
	if badgeLine == "" || cmdLine == "" {
		t.Fatal("badge and command must render on two separate lines")
	}
	for _, c := range []struct {
		name string
		line string
	}{{"badge", badgeLine}, {"command", cmdLine}} {
		lead := len(c.line) - len(strings.TrimLeft(c.line, " "))
		content := lipgloss.Width(c.line) - lead
		want := (bannerWidth - content) / 2
		// Tolerance of one cell: the badge's fill starts with its own space, which
		// reads as indent here but is part of the rendered chip.
		if diff := lead - want; diff < -1 || diff > 1 {
			t.Errorf("%s indent = %d cells, want ~%d (content %d cells)", c.name, lead, want, content)
		}
	}
}
