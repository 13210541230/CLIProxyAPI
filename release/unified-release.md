# Unified Release Workflow (CLIProxyAPI + Management Center + Usage Service)

This document describes the official unified release process implemented in `CLIProxyAPI`.

## Goals

- Publish from `CLIProxyAPI` only.
- Bundle backend binaries and management panel in the same platform artifact.
- Build and bundle `usage-service` from `Cli-Proxy-API-Management-Center/usage-service`.
- Publish all platform assets under one GitHub Release tag.

## Trigger

- Workflow file: `.github/workflows/release.yaml`
- Trigger condition: push tag `v*`

## Build Matrix

- `windows/amd64`
- `windows/arm64`
- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

## Artifact Contents

Each platform package contains:

- `cli-proxy-api` (or `cli-proxy-api.exe`)
- `usage-service` (or `usage-service.exe`)
- `static/management.html`
- `config.example.yaml`
- `README.md`, `README_CN.md`, `LICENSE`
- start script (`start.sh` or `start.bat`)

## Packaging Script

- Script: `release/package-unified.sh`
- Naming format:
  - Windows: `CLIProxyAPI_<version>_<os>_<arch>.zip`
  - Linux/macOS: `CLIProxyAPI_<version>_<os>_<arch>.tar.gz`

## Release Assets

The workflow publishes:

- All platform archives listed above
- `checksums.txt`

## Usage-Service Source of Truth

`usage-service` is built from:

- Repo: `https://github.com/13210541230/Cli-Proxy-API-Management-Center.git`
- Path: `usage-service/cmd/cpa-manager`

This avoids drift between release packages and management-center usage collection logic.

## Operational Notes

- If you need a test release, use a pre-release style tag like `vX.Y.Z-rcN`.
- For production release, push a stable tag like `vX.Y.Z`.
- Keep tag versioning aligned across backend and management-center compatibility expectations.

