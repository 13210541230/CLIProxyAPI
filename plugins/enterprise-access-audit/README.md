# Enterprise Access Audit Plugin

`enterprise-access-audit` is an isolated CPA dynamic-library plugin. It owns per-enterprise-Key model policy, text-only request audit persistence, retention cleanup, and fixed-path Management API handlers. It does not modify CPA core routes, provider routing, translators, `sdk/pluginapi`, or the public plugin ABI.

## Build and package

The module is independently reproducible from `plugins/enterprise-access-audit/go`:

```bash
go test ./...
go test -race ./...
go build ./...
```

On Windows, the checked-in PowerShell entry point runs those checks and packages the native dynamic library in the host's platform directory:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\plugins\enterprise-access-audit\build.ps1
```

The script writes `plugins/<GOOS>/<GOARCH>/enterprise-access-audit.<ext>` by default. Use `-OutputDirectory <path>` to select another staging directory. The output uses CPA's existing C-shared dynamic-library format (`-buildmode=c-shared`), not Go's standard-library-only `-buildmode=plugin` format.

The module is pinned independently in `go/go.mod` and does not add a dependency to the CPA root module. The package command requires a native C toolchain because cgo is required by the C ABI entry point. Build for the same operating-system and architecture that will load the artifact:

| Host platform | Extension | Example output directory |
|---|---|---|
| Windows | `.dll` | `plugins/windows/amd64/` |
| Linux or FreeBSD | `.so` | `plugins/linux/amd64/` |
| macOS | `.dylib` | `plugins/darwin/arm64/` |

Cross-compilation is not assumed. It requires a compatible target C compiler and linker configured for the selected `GOOS`/`GOARCH`; otherwise build on the target platform. The unit-test, race-test, and ordinary package-build commands are portable Go commands, while the dynamic artifact must match the host platform.

## Installation and loading

1. Build the artifact and copy it to the configured CPA plugin directory. The default host directory is `plugins`; the host searches these locations in order:

   ```text
   plugins/<GOOS>/<GOARCH>/enterprise-access-audit.<ext>
   plugins/enterprise-access-audit.<ext>
   ```

2. Keep the filename basename equal to the plugin ID, `enterprise-access-audit`. Do not rename it to include a raw Key, environment secret, or request identifier.
3. Enable dynamic plugins and the plugin instance in the CPA configuration. The host supplies the authenticated Management API context and lifecycle/request metadata:

   ```yaml
   plugins:
     enabled: true
     dir: plugins
     configs:
       enterprise-access-audit:
         enabled: true
         priority: 100
         data_dir: .cli-proxy-api/plugins/enterprise-access-audit
         database_path: .cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite
         retention_days: 30
         default_audit_enabled: true
         max_text_bytes: 32768
         cleanup_interval_seconds: 3600
   ```

4. Restart or reconfigure CPA, then confirm the plugin is enabled through the host plugin Management API. A plugin load or storage failure is an operational error; it is not equivalent to an empty policy.

CPA accepts `.so` on Linux/FreeBSD, `.dylib` on macOS, and `.dll` on Windows. Standard dynamic-library plugins are trusted in-process code. Install only artifacts whose source and build toolchain are trusted as much as the CPA service binary.

## Configuration and SQLite data

The plugin configuration fields are:

| Field | Default | Behavior |
|---|---:|---|
| `data_dir` | `.cli-proxy-api/plugins/enterprise-access-audit` | Relative paths resolve below the CPA working directory; the directory is created during configuration. |
| `database_path` | `<data_dir>/enterprise-access-audit.sqlite` | Relative paths resolve against the CPA working directory; when omitted, the database is placed inside the resolved `data_dir`. Parent directories are created if needed. |
| `retention_days` | `30` | Audit records older than the retention window are removed at startup and periodically. Valid range is 1–3650. |
| `default_audit_enabled` | `true` | Applies to a Key with no stored policy row. A stored per-Key switch always wins. |
| `max_text_bytes` | `32768` | Maximum persisted user-text size. Valid range is 1–1048576 bytes; truncation is recorded as metadata. |
| `cleanup_interval_seconds` | `3600` | Periodic expiry cleanup interval. |

The deterministic default database is `.cli-proxy-api/plugins/enterprise-access-audit/enterprise-access-audit.sqlite` below the CPA working directory. SQLite migrations create policy, settings, audit, and migration tables plus indexes. Back up this database using the operator's normal protected storage process; do not copy API Keys into backup names, notes, or logs.

Policy rows use only the canonical eight-character lower-case API-key hash supplied in execution metadata. Raw API Keys are never accepted, derived, stored, logged, or returned. Model IDs are trimmed, reject whitespace/control characters, ASCII lower-cased, deduplicated, sorted, and matched exactly. An empty deny list allows every model; a new/absent policy is all-model-open with audit enabled by default.

## Phase-1 request behavior

The plugin uses both the host `request_path` metadata and source format. It checks the requested and resolved model, so an alias or model-pool rewrite cannot bypass a deny rule. A denied standard-text execution is terminated before upstream execution with HTTP `403` and stable error type/code `model_not_allowed`. The model catalog remains fully visible; this plugin performs request-time denial only.

Included paths:

| Protocol | Accepted path |
|---|---|
| OpenAI Chat Completions | `/v1/chat/completions` |
| OpenAI legacy Completions | `/v1/completions` |
| OpenAI Responses | `/v1/responses` and `/backend-api/codex/responses` without `execution_session_id` |
| Claude Messages | `/v1/messages` |
| Gemini text | `/v1beta/models/{model}:generateContent` and `:streamGenerateContent` |

Audit records contain the valid Key hash, joined user identity metadata when available, timestamp, model, source format, request ID, outcome, known status code, and only bounded allowlisted user text. Successful, failed, and policy-rejected requests are represented. When the upstream error payload explicitly contains `error.code` or `response.error.code` equal to `cyber_policy`, the failed record is additionally marked with `security_signal: cyber_policy` and may include a bounded, control-character-free `security_message`; generic HTTP 4xx/5xx responses are never inferred as cyber-policy events. Numeric-token legacy prompts are metadata-only with `text_available: false` and `text_unavailable_reason: numeric_prompt`.

The following are explicitly excluded and neither enforce policy nor persist user text: Realtime/WebSocket executions, any request with `execution_session_id`, `/v1/messages/count_tokens`, Gemini `:countTokens`, Responses compact paths, model-list paths, image/video/audio payloads, and every other non-matrix path. System/developer/assistant history, tool parameters, token IDs, media payloads, and raw request JSON are never persisted or returned. A missing or malformed Key hash is outside enterprise-Key scope and is allowed without text persistence.

Audit is observational and manual only. The plugin does not proactively classify content, scan keywords, automatically block suspicious text, suspend accounts, hide model catalog entries, or change provider availability. Operators can filter `security_signal=cyber_policy` to review requests that the upstream explicitly rejected for that policy, inspect the sanitized upstream policy message, and then use the explicit per-Key model deny policy when a restriction is needed.

## Authenticated Management API

The host registers plugin routes below `/v0/management` and supplies Management API authentication. The Management Center must call these routes through its authenticated management client, not through a public provider API Key. All plugin routes are fixed literal paths; identifiers are query/body values because the plugin host does not support dynamic route segments.

```text
GET /v0/management/enterprise-access-audit/policies?key_hash=abcdef12&key_hash=abcdef13
PUT /v0/management/enterprise-access-audit/policies/batch
PUT /v0/management/enterprise-access-audit/policy
GET /v0/management/enterprise-access-audit/audit
GET /v0/management/enterprise-access-audit/audit/detail?id=42
GET /v0/management/enterprise-access-audit/settings
PUT /v0/management/enterprise-access-audit/settings
```

Example request bodies use hashes/placeholders only:

```json
{"key_hashes":["abcdef12"],"denied_models":["gpt-4"],"audit_enabled":null}
{"key_hash":"abcdef12","audit_enabled":false}
{"retention_days":30,"default_audit_enabled":true,"max_text_bytes":32768}
```

`audit_enabled: null` or omitted fields preserve the existing value according to the endpoint contract. Audit responses are bounded text-only records ordered by `created_at DESC, id DESC`, with deterministic pagination and filters for time, hash, model, source format, outcome, and the explicit `security_signal=cyber_policy` marker. A `security_message` field, when present, contains only the bounded sanitized upstream message, never the raw upstream response body. Invalid input, missing details, and unavailable storage use stable 400, 404, and 503 responses.

The Management Center audit page joins hashes to the hash-only usage-service projection:

```text
/v0/management/enterprise/key-bindings/metadata
```

That projection returns only `apiKeyHash`, username, department ID, and email. The audit page must not call the full enterprise key-binding endpoint, public model endpoints, or any endpoint that returns raw API Keys. Username/email/department filtering is performed from this non-secret metadata projection when required.

## Operations and privacy checklist

- Keep the CPA management bearer authentication and Management Center session protected; the plugin adds no second authentication system.
- Treat the SQLite file and audit text as sensitive operational data. Restrict filesystem permissions, back it up through approved storage, and verify retention cleanup after changing `retention_days`.
- Audit is enabled by default for observed enterprise Keys, but a per-Key disable switch prevents new text records. Existing records remain subject to retention cleanup.
- An empty deny list means all models are allowed. A failed plugin load or unavailable database must be investigated rather than interpreted as that default.
- Audit text is bounded and may be truncated; the response exposes truncation metadata. Do not put raw request bodies, authorization headers, or API Keys into issue reports or test output.
- The plugin is phase-1 scope only. Do not treat excluded endpoint absence as a privacy defect or extend it with automatic risk decisions/new endpoints without a separately approved phase.
- Before upgrades, run the module tests and Management Center checks, review `git diff --check`, and verify the frozen CPA route/ABI/translator paths remain unchanged.
