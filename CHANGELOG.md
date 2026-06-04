# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project adheres to
[Semantic Versioning](https://semver.org/).

## [0.3.0] - 2026-06-04

### Added

- **Type-to-filter in the refresh table picker.** The "Select tables to refresh"
  screen now has an always-on search box at the top, mirroring the filter in the
  Move item workspace picker. Start typing and the list narrows to matching
  tables in real time; clear the search and the full grouped hierarchy comes
  back. This makes picking a specific table out of a large model (dozens of
  Dim/Fact tables) fast instead of a long scroll.

  Details:
  - Matching is a case-insensitive substring match on the table name.
  - Group headers (e.g. `All Dim`) stay visible above their matches and show a
    live match count, such as `All Dim (3 matches)`. The global `All tables` row
    is hidden while a filter is active, since "all matches" is not the same as a
    full-model refresh.
  - `space` toggles the row at the cursor. On a group header while filtering, it
    toggles only the currently visible matches — not the whole group.
  - **Selections persist across filtering.** Toggle a table, clear or change the
    search, and your earlier choices stay checked. You can build a selection
    across several different searches before confirming.

### Changed

- Navigation in the refresh picker now uses the arrow and page keys
  (`↑`/`↓`, `alt+↑`/`alt+↓`, `pgup`/`pgdn`) only. The previous single-letter
  shortcuts (`j`/`k` to move, `b` to go back, `q` to quit) were removed because
  the always-on search box now captures those keystrokes. `esc` still goes back
  and `ctrl+c` still quits.

## [0.2.0]

- Earlier releases predate this changelog. See the
  [GitHub releases](https://github.com/DanielAndreassen97/futils/releases) for
  their notes.

[0.3.0]: https://github.com/DanielAndreassen97/futils/releases/tag/v0.3.0
[0.2.0]: https://github.com/DanielAndreassen97/futils/releases/tag/v0.2.0
