# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Common workspaces.** Some workspaces belong to no environment because
  every environment reads from the same one: a shared ingest workspace
  holding a mirrored database, say. List them under **Edit customer → Common
  workspaces** (`common_workspaces` in the config). A reference to an item in
  a common workspace deploys unchanged in every environment, whatever kind of
  reference it is: a lakehouse shortcut, a notebook's default lakehouse, a
  pipeline activity. Shortcuts kept this way are listed under **Shortcuts
  into common workspaces (kept as-is)** in the compare and the HTML report.
  A workspace is either common or on an environment, never both.

- **One question per unknown workspace.** When unresolved references point
  into a workspace that is on no environment and isn't common, the compare
  groups them per workspace, and the mapping flow asks once for each:
  *Common: shared by every environment*, or *Belongs to an environment:
  register it*. Twenty shortcuts into one mirrored database take one answer.
  futils never decides this on its own: a workspace on no environment may
  just as well be a DEV workspace nobody registered, and guessing "shared"
  would deploy TEST pointing at DEV data without a warning.

### Fixed

- **Unresolved shortcut targets no longer claim to be a `Lakehouse`.** The
  type comes from the index when known and is otherwise left empty, so the
  override picker lists every item type (mirrored databases, semantic models
  with OneLake integration, …) instead of hiding the real target.

### Changed

- **Clearer rebind summary in the compare.**
  - Every rebind row says which item(s) carry the reference (`in
    NB_LoadOrders`). More than two items show a count, with up to six names
    on the line below and `+ N more` after that. The HTML report lists them
    all.
  - Item names without a known type prefix (`NB_`, `PL_`, `LH_`, `SM_` …) get
    their type, e.g. `Permissions (SemanticModel)` or `HR_Access
    (SemanticModel)`.
  - The two values of an SQL endpoint rewrite are labelled `host` and
    `endpoint id`, in the terminal and the HTML report.
  - The all-zeros workspace GUID — Fabric's git placeholder for "the workspace
    this item lives in" — reads `(this workspace)` instead of
    `00000000…0000`, in the terminal and the HTML report.
  - Shortcut rows name the target and its type (`Report for … (SemanticModel)`)
    rather than only the target's name, which made a semantic model's OneLake
    tables look like a stray report.
  - Report bindings explain the arrow ("report → semantic model it binds to in
    the target") and mark bindings resolved from a git folder path (`byPath`),
    which have no baseline GUID to show.

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
