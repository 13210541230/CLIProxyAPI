# CLIProxyAPI v6.9.29

Release date: 2026-04-17

## Highlights

- Added management API support for API key model permissions:
  - `GET /v0/management/key-permissions`
  - `GET /v0/management/key-permissions/:key`
  - `PUT /v0/management/key-permissions/:key`
  - `PATCH /v0/management/key-permissions/:key`
  - `DELETE /v0/management/key-permissions/:key`
- Added update-check endpoint for management API:
  - `GET /v0/management/check-update`
- Added management endpoints for active IP monitoring and cleanup:
  - `GET /v0/management/active-ips`
  - `GET /v0/management/ip-statistics`
  - `DELETE /v0/management/inactive-ips`
- Improved `/v1/models` compatibility and metadata behavior:
  - Routes Anthropic-compatible requests to Claude-compatible output shape.
  - Prefers provider-specific model metadata when available.
- Added context-window exposure and override support in model metadata:
  - Configured model context windows can be exposed in listings.
  - OAuth model alias supports `context-window` override.

## Documentation

- Updated `README.md` and `README_CN.md` with the new management API endpoint usage.

## Validation

- Verified with full test run and server build:
  - `go test ./...`
  - `go build -o test-output ./cmd/server`

