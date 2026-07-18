# futils

Interactive CLI for Microsoft Fabric — run notebooks with parameters, refresh semantic-model tables, copy items between workspaces, and deploy a git repo to Fabric with compare-first diffs and name-based reference rebinding.

![demo](demo.gif)

## Features

- **Deploy from git** — deploy a Fabric git repo to target workspaces, straight from origin (never your working tree). The branch is auto-detected from `origin/HEAD` (falling back to `main`, then `master`), or pinned per customer — deploy from `origin/dev`, a release branch, whatever fits your flow. Self-contained: no `parameter.yml` or deployment pipelines needed.
  - **Folder → workspace mappings** per environment: each top-level repo folder deploys to its own workspace, or one mapping covers the whole repo. A customer can deploy from multiple repos, and a first-run guided setup walks you through repo → baseline environment → mappings, challenging cross-environment slips (mapping a TEST folder to a DEV workspace) and rejecting duplicate mappings.
  - **Compare first, always** — every deploy starts as a dry-run: new / changed / orphan summary per mapping, per-part content diffs, and an HTML report (line numbers, folded context) that opens in your browser. Reports are compared on their *semantic* dataset binding, so Fabric's import-time normalization of `definition.pbir` doesn't flag every rebound report as changed forever.
  - **Name-based auto-rebind** — GUIDs baked into git (lakehouses, workspaces, SQL endpoints — in both GUID and name form) are translated to the target environment by item name: notebooks, Direct Lake semantic models (OneLake and SQL-endpoint flavours), and report dataset bindings. References that can't be resolved are listed with **likely causes** and fixed interactively: register the workspace they live in, map an override to a specific item, or ignore.
  - **Reference overrides & custom substitutions** — environment-agnostic find→replace rules (literal or resolved-by-name, optionally regex) for anything the automatic pass doesn't cover.
  - **Per-mapping baseline workspaces** — isolate a mapping's reference resolution to a single baseline/target workspace pair when the environment-wide lookup would be ambiguous.
  - **Cherry-pick & orphan cleanup** — select exactly which items deploy; items that exist only in the target are flagged as orphans and can be deleted in a separate, separately-confirmed pass. Data-bearing orphans (lakehouses, warehouses, …) take an extra confirm that names each one, since deleting them destroys their tables and files.
  - **Workspace folders** — new items land in the workspace folder that mirrors their path in the repo (Fabric's own git-sync doesn't reproduce folders); the folder tree is created as needed. Existing items aren't moved.
  - **Lakehouse shortcuts** — OneLake shortcut targets in `shortcuts.metadata.json` are rebound to the target environment by name, so an internal shortcut follows the promotion instead of pointing back at dev. External targets (ADLS/S3/…) are left untouched.
  - **Pipeline dependency order** — data pipelines that invoke other repo pipelines publish in dependency order, so the invoked pipeline exists before the invoker references it.
  - **Schedules toggle** — optionally keep schedules out of compare and deploy, so schedules configured directly in TEST/PROD survive.
  - **Two backends** — stable per-item create/update (default), or the bulk-import backend (Fabric preview API) as an opt-in per customer.
  - **Variable libraries** — VariableLibrary items deploy first (anything may reference them), and after publish futils activates the value set named after the target environment — the same convention as fabric-cicd. No matching value set? The target's active-set choice is left untouched, by design.
  - **Environments** — a deployed Environment only *stages* its sparkcompute settings and libraries; after the deploy, futils offers to run the environment publish (it can take minutes) and tracks it to completion. Declining leaves it staged for a manual publish.
  - **Shell types done right** — Warehouse/SQLDatabase and other definitionless items are created as shells with their `.platform` `creationPayload` (collation, schema support) intact, compared on description instead of producing unverifiable rows, and a freshly created Lakehouse is held until its SQL analytics endpoint finishes provisioning so later rebinds resolve.
  - **Notebook formats** — `.ipynb`-form notebooks publish with `format=ipynb`, and definition parts are ordered the way the notebook API requires (content before settings).
  - **Post-deploy runs** — register notebooks and data pipelines to be offered for execution right after a successful deploy.
  - **Deploy history** — a timestamped HTML deploy report written to a repo folder after each real deploy.
- **Schema compare** — compare lakehouse table schemas between two workspaces (lakehouses paired by name) and see added/removed/changed tables and columns before you promote.
- **Run notebooks** — pick a customer, environment, and notebook, override Papermill parameters, and submit a `RunNotebook` job. Polls until completion and reports status.
- **Run pipelines** — pick a data pipeline the same way and trigger a pipeline job, polled to completion.
- **Refresh tables** — pick a semantic model, multi-select tables (with group toggles and type-to-filter search), and trigger an [Enhanced Refresh](https://learn.microsoft.com/en-us/power-bi/connect-data/asynchronous-refresh) job. Filters out calculated tables and calculation groups automatically.
- **Move items** — copy a Report, Semantic Model, or Notebook from one workspace to another. For Reports, optionally rebind to a different semantic model in the destination.
- **Favourites** — pin the notebooks and parameters you actually use, so the run flow surfaces them first instead of scrolling through 200+ items.
- **Per-customer config** — multiple customers, each with an environment ladder (e.g. `DEV, TEST, PROD`) where every environment maps to one or more workspaces (e.g. a Config workspace and a SemMod workspace).
- **Friendly TUI** — numbered menus (`1`–`9` jump straight to an option), type-to-filter pickers, inline descriptions, and `?` info boxes explaining every setting. `esc` goes back, `m` returns to the main menu, `q` quits — a key legend is always visible.
- **Sandboxed config** — point `FUTILS_CONFIG` at an alternate config file to try flows (or demo the tool) without touching your real setup.
- **OAuth2 browser auth** — Entra ID via the Azure CLI public client. Tokens cached in your OS keychain, silently refreshed for months.
- **Resilient API client** — automatic retry with backoff on throttling (live countdown in the spinner) and transient network failures.
- **Cross-platform** — macOS, Linux, Windows.

## Install

### Homebrew (macOS/Linux)

```sh
brew install DanielAndreassen97/tap/futils
```

### Scoop (Windows)

```powershell
scoop bucket add futils https://github.com/DanielAndreassen97/scoop-bucket
scoop install futils
```

### Go

```sh
go install github.com/DanielAndreassen97/futils@latest
```

### Download binary

Download from [GitHub Releases](https://github.com/DanielAndreassen97/futils/releases/latest) and add to your PATH.

### If macOS says the app can't be verified

Release binaries aren't Developer ID-signed or notarized yet, so recent macOS versions may warn the first time you run `futils` — even when installed via Homebrew. If Gatekeeper blocks it:

- approve it under **System Settings → Privacy & Security → "Open Anyway"**, or
- clear the quarantine flag (relevant when the binary was downloaded with a browser):

  ```sh
  xattr -d com.apple.quarantine "$(which futils)"
  ```

Code signing and notarization are planned. In the meantime you can cryptographically verify any release yourself — see below.

### Verifying releases

Every release ships with a `checksums.txt`, an SPDX SBOM per archive, and a signed [SLSA build provenance](https://slsa.dev/) attestation. You can cryptographically verify that a downloaded artifact was built by this repo's release workflow (and nowhere else) with the GitHub CLI:

```sh
gh attestation verify futils_Darwin_arm64.tar.gz --repo DanielAndreassen97/futils
```

A successful verification proves the file's provenance back to the tagged commit and workflow — useful since this tool handles your Entra ID OAuth tokens.

## Try it — no tenant required

futils ships a demo mode: a self-contained fake Fabric tenant for a fictional customer, with three environments that tell a coherent DEV → TEST → PROD story. Every flow works against it offline — including a full deploy with compare, auto-rebind, and post-deploy runs.

```sh
futils demoseed        # seed a sandbox config + git repo under /tmp/futils-demo
export FUTILS_DEMO=1 FUTILS_CONFIG=/tmp/futils-demo/config.json
futils                 # explore everything, no tenant or login needed
```

## Prerequisites

- A workspace on **Fabric (F SKU)**, **Premium (P SKU)**, **Premium Per User (PPU)**, or **Embedded (A/EM SKU)** capacity. Notebook execution and item copy use Fabric APIs that aren't available on Power BI Pro.
- An Entra ID account with Contributor (or higher) permissions on the workspaces you target.
- For **Deploy**: a local clone of a [Fabric git-integrated](https://learn.microsoft.com/en-us/fabric/cicd/git-integration/intro-to-git-integration) repo. futils deploys what's on origin (default branch, or one you pin per customer), so commit and push first.

## Usage

```sh
futils                # Interactive menu (Actions / Settings)
futils run            # Run a notebook
futils runpipeline    # Run a data pipeline
futils refresh        # Refresh semantic-model tables
futils move           # Copy an item between workspaces
futils deploy         # Deploy a Fabric git repo to target workspaces
futils schemacompare  # Compare lakehouse schemas between workspaces
futils favourites     # Manage favourite notebooks/parameters
futils add            # Add a customer
futils edit           # Edit a customer (environments, deploy setup, favourites)
futils remove         # Remove a customer
futils list           # List customers
futils logout         # Clear cached OAuth tokens
futils version        # Show version
futils help           # Show available commands
```

## Configuration

Config is stored at `~/.config/futils/config.json` (macOS/Linux) or `%APPDATA%\futils\config.json` (Windows). Set `FUTILS_CONFIG=/path/to/other.json` to use an alternate file (handy for demos and testing).

Everything is managed from the TUI (`futils` → Manage customers). Each customer has:

- **Environments** — an alias (e.g. `DEV`, `TEST`, `PROD`) mapping to one or more workspace names (e.g. `DW - TEST - Config`, `DW - TEST - SemMod`). Reference-only workspaces (looked up during rebind, never deployed to) belong here too.
- **Deploy setup** (all optional, only needed for the deploy flow) — primary repo path, deploy branch (auto-detected or pinned), baseline environment (which env the git code belongs to), per-environment folder→workspace mappings, excluded item types, schedules toggle, bulk-import backend toggle, reference overrides, and custom substitutions.
- **After deploy** — post-deploy notebook runs and a deploy-history folder.
- **Favourites** — pinned notebooks and parameters for the run flow.

First-time deploy setup is guided: pick the repo, pick the baseline environment, and map folders to workspaces — futils saves the answers to config as you go.

## How it works

### Deploy
1. Reads Fabric items from `origin/<branch>` of the mapped repo(s) — the working tree is never deployed. The branch is the remote's default unless the customer pins one.
2. Resolves each folder→workspace mapping and compares every local item against the target workspace: new, changed (per-part content diff), or orphan (exists only in the target).
3. Translates baseline-environment references to the target environment by item name: lakehouse/workspace/SQL-endpoint GUIDs in notebooks, Direct Lake connections in semantic models (OneLake and SQL-endpoint forms), and report dataset bindings in `definition.pbir`. Unresolved references are surfaced with likely causes and can be fixed interactively.
4. Shows the full compare (optionally as an HTML diff report in the browser) and asks before continuing — every deploy is a dry-run until you confirm.
5. Publishes the selected items via [item create/update definition](https://learn.microsoft.com/en-us/rest/api/fabric/core/items) (or the bulk-import preview API), then offers orphan deletion as a separate confirmed pass, post-deploy notebook runs, and writes a timestamped HTML report to the deploy-history folder.

### Run notebooks
1. Authenticates via browser-based OAuth2 with Microsoft Entra ID (Fabric scope).
2. Resolves the workspace by name → lists notebooks in the workspace.
3. Fetches the notebook's `.ipynb` definition via the [Fabric getDefinition API](https://learn.microsoft.com/en-us/rest/api/fabric/notebook/items/get-notebook-definition).
4. Parses the Papermill-tagged parameter cell so you can override values before submitting.
5. Submits a [RunNotebook job](https://learn.microsoft.com/en-us/rest/api/fabric/notebook/job-scheduler/run-on-demand-notebook-job) and polls until completion.

### Refresh tables
1. Picks customer → environment → semantic model.
2. Fetches the model's TMDL definition via [Fabric getDefinition](https://learn.microsoft.com/en-us/rest/api/fabric/semanticmodel/items/get-semantic-model-definition) to discover all tables.
3. Filters tables by partition type — only tables with `partition = m` (Power Query / import) are refreshable; calculated tables, calculation groups, and measure-only tables are excluded.
4. Multi-select picker with auto-grouping by prefix (`Dim`, `Fakta`/`Fact`, `Log`, `Other`), an "All tables" shortcut, and type-to-filter search.
5. Triggers an [Enhanced Refresh](https://learn.microsoft.com/en-us/power-bi/connect-data/asynchronous-refresh) (`type=full`, `commitMode=transactional`) and polls until completion.

### Move items
1. Picks source workspace + item.
2. Fetches the item definition (`getDefinition`) — Reports, Semantic Models, and Notebooks all expose this.
3. Picks the destination workspace.
4. Creates the item in the destination via [`POST /workspaces/{id}/items`](https://learn.microsoft.com/en-us/rest/api/fabric/core/items/create-item).
5. For Reports, optionally rebinds the report's dataset reference to a semantic model that exists in the destination workspace.

### APIs used

| API | Purpose |
|-----|---------|
| **Fabric Items API** (`api.fabric.microsoft.com`) | Workspace resolution, item listing, `getDefinition`, item create/update/delete, bulk import (preview), notebook run + status, lakehouse SQL-endpoint lookup |
| **Power BI REST API** (`api.powerbi.com`) | Enhanced Refresh trigger + polling, Report rebind |
| **OneLake** | Lakehouse table schemas for Schema compare |
| **Microsoft Entra ID** | OAuth2 authentication with token caching |

## Authentication

Uses OAuth2 Authorization Code Flow with the Azure CLI public client ID. On first use per customer, a browser window opens for Microsoft login. Tokens are cached in your OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service) and silently refreshed — you typically authenticate once and stay logged in for months.

Use `futils logout` to clear all cached credentials.

## License

MIT
