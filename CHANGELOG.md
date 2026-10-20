# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and the project adheres to
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- **Shared OneLake shortcut targets are no longer rebound to the wrong item.**
  A lakehouse shortcut into a workspace outside every environment — e.g. a
  mirrored database in a shared ingest workspace that dev, test and prod all
  read from — is the same target in every env. It used to surface as an
  unresolved reference, and the deploy prompt steered toward "Map it to a
  specific item". Because a reference override stores only a name and resolves
  it in the *target* env, mapping such a target could rebind the shortcut onto
  a same-named item there — even onto the lakehouse that owns the shortcut.
  Now such a shortcut deploys byte-identical and is listed under a new
  **Shared shortcut targets (kept as-is)** section of the compare. An explicit
  reference override still wins.

  Details:
  - The decision uses the shortcut's own `workspaceId`: the target is shared
    when the item is unknown to both envs *and* its workspace belongs to
    neither. A target inside a baseline workspace that can't be resolved is
    still reported as unresolved.
  - Unresolved shortcut targets no longer claim to be a `Lakehouse`. The type
    comes from the index when known and is otherwise left empty, so the
    override picker lists every item type (mirrored databases, semantic models
    with OneLake integration, …) instead of hiding the real target.

### Changed

- **Clearer rebind summary in the compare.**
  - Every rebind row says which item(s) carry the reference (`in
    DataplatformOperations`). More than two items show a count, with the names
    listed on the line below. The HTML report lists them all.
  - Item names without a type prefix (`NB_`, `PL_`, `LH_` …) get their type,
    e.g. `Permissions (SemanticModel)`.
  - The two values of an SQL endpoint rewrite are labelled `host` and
    `endpoint id`.
  - The all-zeros workspace GUID — Fabric's git placeholder for "the workspace
    this item lives in" — reads `(this workspace)` instead of
    `00000000…0000`.
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
