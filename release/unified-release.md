# Unified Release Workflow (CLIProxyAPI + CPA-Manager)

This document describes the unified native release published by the authoritative
CLIProxyAPI fork:

`https://github.com/13210541230/CLIProxyAPI`

## Goals

- Publish the product from the CLIProxyAPI fork only.
- Bundle CLIProxyAPI and the CPA-Manager control plane in one platform archive.
- Embed the built `management.html` in the CPA-Manager binary.
- Include the companion updater used for verified local replacement and rollback.
- Publish a machine-readable `manifest.json` with SHA-256 checksums.

The two source repositories remain separate. The CLIProxyAPI release workflow
clones the Management Center fork at the immutable `MANAGEMENT_CENTER_REF`
commit, builds CPA-Manager, and packages both products under one release tag.
Advance that full SHA in the CLIProxyAPI fork whenever a new Management Center
commit is ready for a suite release.

## Trigger

- Workflow file: `.github/workflows/release.yaml`
- Trigger condition: push a tag such as `v7.2.146`

## Build Matrix

- `windows/amd64`
- `windows/arm64`
- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

The release may also contain Linux no-plugin and FreeBSD compatibility archives.
Those compatibility assets are not selected by the native self-update manifest.

## Artifact Contents

Each native update package contains:

- `cli-proxy-api` or `cli-proxy-api.exe`
- `cpa-manager` or `cpa-manager.exe`
- `cpa-updater` or `cpa-updater.exe`
- `suite-version.json` with the CPA and CPA-Manager versions
- `config.example.yaml`, `README.md`, `README_CN.md`, `SUITE-README.md`, `SUITE-README_CN.md`, and `LICENSE`
- Windows: `start.bat`, `start.ps1`, and `stop.bat`
- Linux/macOS: `start.sh` and `stop.sh`

The CPA-Manager binary contains the generated management page. The package does
not require a separate `management.html` replacement for the embedded-panel mode.

## Packaging

- Script: `release/package-unified.sh`
- Archive naming:
  - Windows: `CLIProxyAPI-Suite_<version>_<os>_<arch>.zip`
  - Linux/macOS: `CLIProxyAPI-Suite_<version>_<os>_<arch>.tar.gz`
  - `<arch>` is `amd64` or `aarch64` in the release filename.

The package script requires both the CPA-Manager and `cpa-updater` binaries.
It can import the existing CPA archive contents with `--server-package-dir`
so plugin files and compatibility documentation remain in the suite package.
The optional `static/management.html` copy is retained only as a compatibility
artifact; the production panel is embedded in CPA-Manager.

## Update Manifest

The workflow publishes `manifest.json` and `suite-checksums.txt` in the same
GitHub Release. It is read by CPA-Manager from the canonical repository and contains:

- CPA version
- CPA-Manager version
- release tag and URL
- one SHA-256-verified archive for each supported native OS/architecture pair

The manifest uses runtime architecture names (`arm64`) even though CPA release
archive names use `aarch64`.

## Embedded HTML normalization

Before building CPA-Manager, generated panel HTML is normalized by:

`bin/release/normalize-embedded-html.py`

The normalization converts line endings and known hidden/control code points into
equivalent JavaScript `\\uXXXX` escapes. This keeps YAML parser and
`repoSourceIntegrity` behavior stable in production bundles.

## Source of Truth

- Release repository: `13210541230/CLIProxyAPI`
- Management Center source used by the workflow:
  `13210541230/Cli-Proxy-API-Management-Center`
- Embedded backend entry point: `usage-service/cmd/cpa-manager`
- Updater entry point: `usage-service/cmd/cpa-updater`

## Runtime Model

The launcher starts CLIProxyAPI first with `config.yaml` (creating it from
`config.example.yaml` on first use), then starts CPA-Manager with automatic CPA
startup suppressed. It accepts `--config <path>` for a custom configuration and
records both process IDs, executable paths, exact arguments, working directories,
and the selected config path in `.suite-runtime.json`. The matching stop script
terminates only processes whose PID and executable path match that recorded state.

The Management Center System page performs manual version checks and package
downloads only. With both services running from the launcher, running
`cpa-updater` with no arguments (or double-clicking `cpa-updater.exe`) performs
automatic installation; `--check` only checks for a newer version. The updater:

1. Fetches the canonical release manifest and checks both installed versions.
2. Downloads the current platform archive, verifies SHA-256, and safely extracts it.
3. Stops the recorded CPA and CPA-Manager processes.
4. Replaces the CPA, Manager, updater, version metadata, launch scripts, and bundled program assets with rollback backups.
5. Preserves `config.yaml`, auth files, databases, logs, and local plugin settings.
6. Restarts both services with the saved configuration and arguments and checks CPA and Manager health.
7. Restores the previous program assets and restarts the old suite if replacement or health validation fails.

## Operational Notes

- Run a test with a pre-release tag such as `v7.2.146-rc1`.
- Use stable tags for production releases.
- Keep the CPA-Manager source changes available in the Management Center fork
  before publishing the CPA release.
- Do not use arbitrary download URLs; the updater accepts only HTTPS assets from
  the canonical CLIProxyAPI repository.
