// futils is an interactive CLI for Microsoft Fabric. It runs notebooks
// with parameter overrides, refreshes semantic-model tables, copies
// items between workspaces, and manages per-customer workspace configs.
// Authentication is via Entra ID (Azure CLI public client) with tokens
// cached in the OS keychain.
//
// Usage:
//
//	futils               # interactive menu
//	futils run           # run a notebook
//	futils refresh       # refresh semantic-model tables
//	futils move          # copy an item between workspaces
//	futils --version     # print version
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/DanielAndreassen97/futils/cmd"
	"github.com/DanielAndreassen97/futils/internal/config"
	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/DanielAndreassen97/futils/internal/ui"
	"github.com/DanielAndreassen97/futils/internal/update"
)

// version is overridden by the release build via -ldflags.
var version = "dev"

func main() {
	ui.Version = version
	fabric.SetUserAgent(version)
	// FUTILS_DEMO=1 swaps the Fabric API for a self-contained fake tenant —
	// every flow works offline. Seed the matching config and git repo with
	// `futils demoseed`.
	if os.Getenv("FUTILS_DEMO") != "" {
		cmd.EnableDemoMode()
		ui.DemoMode = true
	}
	configPath := config.GetConfigPath()
	args := os.Args[1:]

	if len(args) == 0 {
		// Build provenance is only read by the banner, which only the
		// interactive menu renders — so compute it here rather than on
		// every subcommand's startup path.
		bt, rev, mod := buildProvenance()
		ui.BuildInfo = bannerBuildInfo(version, bt, rev, mod)
		// Best-effort update hint under the banner. Skipped in demo mode and
		// on dev builds; a fresh cache answers instantly, a live check gets a
		// 600ms budget and otherwise lands in the cache for the next launch.
		if os.Getenv("FUTILS_DEMO") == "" {
			ui.UpdateNotice = update.Notice(version, 600*time.Millisecond)
		}
		cmd.MainMenu(configPath)
		return
	}

	var err error
	switch args[0] {
	case "run":
		err = cmd.Run(configPath)
	case "runpipeline", "run-pipeline":
		err = cmd.RunPipelineCmd(configPath)
	case "refresh":
		err = cmd.Refresh(configPath)
	case "move":
		err = cmd.Move(configPath)
	case "deploy":
		err = cmd.Deploy(configPath)
	case "schemacompare", "schema-compare":
		err = cmd.SchemaCompare(configPath)
	case "favorites", "favourites":
		err = cmd.Favorites(configPath)
	case "add":
		err = cmd.Add(configPath)
	case "edit":
		err = cmd.Edit(configPath)
	case "remove":
		err = cmd.Remove(configPath)
	case "list":
		err = cmd.List(configPath)
	case "logout":
		err = cmd.Logout(configPath)
	case "demoseed":
		var dir string
		if len(args) > 1 {
			dir = args[1]
		}
		err = cmd.DemoSeed(dir)
	case "help", "--help", "-h":
		cmd.Help()
	case "version", "--version", "-v":
		fmt.Println(versionString())
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", args[0])
		os.Exit(1)
	}
	if err != nil {
		// ErrQuit and ErrGoBack surface when the user presses ctrl+c or
		// esc — exit quietly rather than printing a confusing error.
		if errors.Is(err, ui.ErrQuit) || errors.Is(err, ui.ErrGoBack) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// buildProvenance reads where this binary came from: its own mtime (the
// build time) plus the git revision and dirty flag that `go build` embeds.
func buildProvenance() (buildTime time.Time, revision string, modified bool) {
	if exe, err := os.Executable(); err == nil {
		if fi, statErr := os.Stat(exe); statErr == nil {
			buildTime = fi.ModTime()
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	return
}

func versionString() string {
	bt, rev, mod := buildProvenance()
	return formatVersion(version, bt, rev, mod)
}

// shortRevision trims a git SHA to 7 chars for compact display.
func shortRevision(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}

// bannerBuildInfo is the compact one-line freshness hint shown under the
// banner for dev builds (empty for release, where the tag says it all):
// "built 2006-01-02 15:04 · <short-sha>" with a trailing * for a dirty tree.
func bannerBuildInfo(version string, buildTime time.Time, revision string, modified bool) string {
	if version != "dev" {
		return ""
	}
	var parts []string
	if !buildTime.IsZero() {
		parts = append(parts, "built "+buildTime.Format("2006-01-02 15:04"))
	}
	if revision != "" {
		short := shortRevision(revision)
		if modified {
			short += "*"
		}
		parts = append(parts, short)
	}
	return strings.Join(parts, " · ")
}

// formatVersion renders the version line. Pure (no I/O) so it's unit-tested.
// Release builds print just "futils <tag>"; dev builds append build time and
// the short git revision (marked "(modified)" when built from a dirty tree).
func formatVersion(version string, buildTime time.Time, revision string, modified bool) string {
	if version != "dev" {
		return "futils " + version
	}
	var b strings.Builder
	b.WriteString("futils dev")
	if !buildTime.IsZero() {
		fmt.Fprintf(&b, "\n  built:    %s", buildTime.Format("2006-01-02 15:04:05"))
	}
	if revision != "" {
		fmt.Fprintf(&b, "\n  revision: %s", shortRevision(revision))
		if modified {
			b.WriteString(" (modified)")
		}
	}
	return b.String()
}
