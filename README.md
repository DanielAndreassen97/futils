# futils

Interactive CLI for Microsoft Fabric — run notebooks with parameters, refresh semantic-model tables, copy items between workspaces, and manage per-customer workspace configs.

![demo](demo.gif)

## Features

- **Run notebooks** — pick a customer, environment, and notebook, override Papermill parameters, and submit a `RunNotebook` job. Polls until completion and reports status.
- **Refresh tables** — pick a semantic model, multi-select tables (with `All Dim` / `All Fakta` / `All Log` group toggles), and trigger an [Enhanced Refresh](https://learn.microsoft.com/en-us/power-bi/connect-data/asynchronous-refresh) job. Filters out calculated tables and calculation groups automatically.
- **Move items** — copy a Report, Semantic Model, or Notebook from one workspace to another. For Reports, optionally rebind to a different semantic model in the destination.
- **Favourites** — pin the notebooks and parameters you actually use, so the run flow surfaces them first instead of scrolling through 200+ items.
- **Per-customer config** — multiple customers, each with their own workspace pattern (e.g. `DW - {env} - DataMart`) and environment ladder (e.g. `DEV, TEST, PROD`).
- **Workspace aliases** — per-environment override when the pattern doesn't fit (e.g. `PROD → "Production Reports"`).
- **OAuth2 browser auth** — Entra ID via the Azure CLI public client. Tokens cached in your OS keychain, silently refreshed for months.
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

### Verifying releases

Every release ships with a `checksums.txt`, an SPDX SBOM per archive, and a signed [SLSA build provenance](https://slsa.dev/) attestation. You can cryptographically verify that a downloaded artifact was built by this repo's release workflow (and nowhere else) with the GitHub CLI:

```sh
gh attestation verify futils_Darwin_arm64.tar.gz --repo DanielAndreassen97/futils
```

A successful verification proves the file's provenance back to the tagged commit and workflow — useful since this tool handles your Entra ID OAuth tokens.

## Prerequisites

- A workspace on **Fabric (F SKU)**, **Premium (P SKU)**, **Premium Per User (PPU)**, or **Embedded (A/EM SKU)** capacity. Notebook execution and item copy use Fabric APIs that aren't available on Power BI Pro.
- An Entra ID account with Contributor (or higher) permissions on the workspaces you target.

## Usage

```sh
futils              # Interactive menu (Actions / Settings)
futils run          # Run a notebook
futils refresh      # Refresh semantic-model tables
futils move         # Copy an item between workspaces
futils favourites   # Manage favourite notebooks/parameters
futils add          # Add a customer
futils edit         # Edit a customer (workspaces, environments, aliases)
futils remove       # Remove a customer
futils list         # List customers
futils logout       # Clear cached OAuth tokens
futils version      # Show version
futils help         # Show available commands
```

## Configuration

Config is stored at `~/.config/futils/config.json` (macOS/Linux) or `%APPDATA%\futils\config.json` (Windows).

Each customer needs:
- **Workspace pattern** — Power BI workspace name with `{env}` placeholder (e.g. `DW - {env} - DataMart`)
- **Environments** — list of environments (e.g. `DEV, TEST, PROD`)
- **Aliases** (optional) — per-environment workspace name override when the pattern doesn't fit

## How it works

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
4. Multi-select picker with auto-grouping by prefix (`Dim`, `Fakta`/`Fact`, `Log`, `Other`) and an "All tables" shortcut.
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
| **Fabric Items API** (`api.fabric.microsoft.com`) | Workspace resolution, item listing, `getDefinition` (notebook + semantic model), item create, notebook run + status |
| **Power BI REST API** (`api.powerbi.com`) | Enhanced Refresh trigger + polling, Report rebind |
| **Microsoft Entra ID** | OAuth2 authentication with token caching |

## Authentication

Uses OAuth2 Authorization Code Flow with the Azure CLI public client ID. On first use per customer, a browser window opens for Microsoft login. Tokens are cached in your OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service) and silently refreshed — you typically authenticate once and stay logged in for months.

Use `futils logout` to clear all cached credentials.

## License

MIT
