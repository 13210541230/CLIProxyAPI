# CLIProxyAPI v7.0.7 Release Notes

## Highlights

- Bundle CPA Management Center panel into Windows package (`static/management.html`) for direct local management UI access.
- Include monitoring enhancements from CPAMC build `abb685a`:
  - API Key alias display format: `别名（hash后6位）`
  - API Key usage summary table with sort and TopN controls
  - API Key trend comparison panel (tokens/requests/cost)
  - Usage query supports `fromMs`/`toMs`/`apiKeyHash` filters

## Integration Validation

Validated against local management API on port `8327` with secret-key auth:

- `GET /v0/management/check-update`
- `GET /v0/management/key-permissions`
- `PUT /v0/management/key-permissions/:key`
- `GET /v0/management/key-permissions/:key`
- `DELETE /v0/management/key-permissions/:key`

## Artifacts

- `CLIProxyAPI_7.0.7_windows_amd64_mc.zip`
