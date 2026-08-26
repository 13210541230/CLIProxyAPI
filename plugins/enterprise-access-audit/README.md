# Enterprise Access Audit Plugin

This directory contains an isolated Go module for the enterprise access and audit plugin. The module owns policy and audit persistence and does not modify CLIProxyAPI routes, the public plugin ABI, or translators.

## Build

From `plugins/enterprise-access-audit/go`:

```bash
go test ./...
go test -race ./...
go build ./...
```

Build a native CPA plugin on platforms that support Go's plugin mode:

```bash
go build -buildmode=plugin -o enterprise-access-audit.so .
```

The module pins its own `modernc.org/sqlite` dependency and can be built independently of the CPA root module.

## Configuration

The plugin accepts the host plugin lifecycle YAML envelope. Supported fields are:

```yaml
data_dir: .cli-proxy-api/plugins/enterprise-access-audit
database_path: enterprise-access-audit.sqlite
retention_days: 30
default_audit_enabled: true
max_text_bytes: 32768
cleanup_interval_seconds: 3600
```

The deterministic default data directory is `.cli-proxy-api/plugins/enterprise-access-audit` below the CPA working directory. A relative `data_dir` or `database_path` is resolved against that working directory; the database parent directory is created during configuration. Retention is bounded to 1–3650 days and text storage to 1–1048576 bytes.

## Persistence contract

Policy rows are keyed only by the canonical eight-character lower-case API-key hash already supplied in execution metadata. Raw API keys are not accepted, derived, or persisted by this module. Model IDs are trimmed, reject control or whitespace characters, ASCII lower-cased, deduplicated, and sorted. An empty deny list allows every model; an absent policy has audit enabled.

SQLite migrations create the policy, settings, audit, and migration tables plus indexes. Policy batch replacement uses one transaction and preserves fields omitted by a patch. Lifecycle state uses a read/write lease so reconfiguration and shutdown close retired databases only after all operations release their read lease.

## Phase-1 request enforcement and audit

The plugin now intercepts `request.intercept_before` and `request.intercept_after` using both `request_path` and `SourceFormat`. It accepts only standard text execution paths: OpenAI chat/completions/responses, Codex responses, Claude messages, and Gemini generate/streamGenerateContent. Any `execution_session_id`, count-token path, compact path, model-list path, media path, or other path is ignored.

A valid eight-character API-key hash in `quota_key_hash` selects the per-Key exact normalized deny list. The requested and resolved models are both checked; a match returns HTTP 403 with `model_not_allowed` before upstream execution. Missing or malformed hashes pass through without storing user text.

Audit drafts and lifecycle finals are correlated by `RequestID`. Only allowlisted user text is stored, bounded by `max_text_bytes`; system/developer/assistant/tool/media values and raw JSON are never persisted. Numeric-token legacy completion prompts produce a metadata-only record with `text_unavailable_reason=numeric_prompt`. Completion outcomes are `succeeded`, `failed`, `rejected`, or `canceled`.

## Authenticated Management API

The host registers these fixed literal routes below `/v0/management`; Management API authentication remains owned by the host. No route contains a dynamic path parameter. Policy identifiers are canonical eight-character API-key hashes only.

- `GET /enterprise-access-audit/policies?key_hash=abcdef12&key_hash=abcdef13`
- `PUT /enterprise-access-audit/policies/batch` with `{"key_hashes":["abcdef12"],"denied_models":["gpt-4"],"audit_enabled":null}`; `null` preserves each existing audit switch.
- `PUT /enterprise-access-audit/policy` with `{"key_hash":"abcdef12","audit_enabled":false}`; omitted fields are preserved.
- `GET /enterprise-access-audit/audit?model=gpt-4&outcome=failed&page=1&page_size=50`
- `GET /enterprise-access-audit/audit/detail?id=42`
- `GET /enterprise-access-audit/settings`
- `PUT /enterprise-access-audit/settings` with `{"retention_days":30,"default_audit_enabled":true,"max_text_bytes":32768}`.

Audit responses are bounded text-only records ordered by `created_at DESC, id DESC`. Numeric-token prompts return `text:""`, `text_available:false`, and `text_unavailable_reason:"numeric_prompt"`; token IDs, raw request JSON, and media/tool payloads are never returned. Invalid input, missing details, and unavailable storage use stable JSON error codes with HTTP 400, 404, and 503 respectively.
