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
- `config.example.yaml`
- `README.md`, `README_CN.md`, `LICENSE`
- `start.sh` or `start.bat` that starts CPA-Manager as the single entry point

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

CPA-Manager is the local control plane. Its local runtime configuration stores the
CPA executable path, working directory, arguments, and auto-start preference. An
empty executable path resolves to `cli-proxy-api` beside CPA-Manager, allowing a
fresh unified package to be launched through one `start.*` script.

The update flow is:

1. Fetch `manifest.json` from the canonical release.
2. Download the current platform archive.
3. Verify SHA-256 and safely extract the archive.
4. Start `cpa-updater` as a separate process.
5. Stop the managed CPA and CPA-Manager processes.
6. Replace binaries with backup copies.
7. Start the updated processes and check CPA-Manager `/health`.
8. Restore the backups and restart the old processes if health validation fails.

## Operational Notes

- Run a test with a pre-release tag such as `v7.2.146-rc1`.
- Use stable tags for production releases.
- Keep the CPA-Manager source changes available in the Management Center fork
  before publishing the CPA release.
- Do not use arbitrary download URLs; the updater accepts only HTTPS assets from
  the canonical CLIProxyAPI repository.
