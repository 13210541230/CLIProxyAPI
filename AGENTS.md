# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/modeltrace/` — Native, management-authenticated Codex model attribution; config-adjacent bounded state, two runs/six challenges maximum; see `docs/modeltrace.md`
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/home/` — CLIProxyAPIHome control plane integration (bootstrap, RESP communication, dispatch coordination)
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Payload configuration MUST be the final semantic barrier before sending requests in every executor, including streaming, WebSocket, continuation, retry/fallback, image, and token-count paths where applicable. Complete all built-in payload translation, normalization, injection, and cleanup first, then evaluate and apply user payload rules exactly once to the final business payload. No subsequent logic may overwrite configured values or restore filtered fields. Only necessary transport framing, serialization, signature calculation, and read-only validation may follow without changing business payload semantics. Add regression tests when introducing or changing request-building paths to enforce this ordering.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- Note: if modifying features that involve CLIProxyAPIHome, check if corresponding updates are needed in the CLIProxyAPIHome repository.

## Upstream v8 merge record
- This fork tracks upstream `CLIProxyAPI/v8`. Merge upstream into the `upstream-v8` branch first, run `scripts/windows/verify-upstream-v8-merge.ps1`, then fast-forward `main`; never merge upstream straight into `main`.
- v7 → v8 changed the module path to `/v8`, renamed config paths (`oauth.providers.*` → `upstream.*`), and added the `/v8/management` API surface while freezing `/v0/management`. `/v0` compatibility is kept by reusing upstream's frozen registration plus fork-only endpoints; new features belong under `/v8/management`.
- Register every fork-only config key in `internal/config/config_v8.go` (`v8Paths`, legacy shared paths, and `v8AllowedRoots` for whole sections such as `quota`) — the v8 migration comments out unregistered sections instead of failing, which silently drops settings. `internal/config/config_v8_fork_fields_test.go` guards the gateway, quota, and plugin fields across migration and re-migration.
- `identity-confuse` was removed in favour of upstream `upstream/*/header-defaults` (including `timezone` and `stabilize-device-profile`); do not reintroduce a fork-local identity implementation.
- Keep upstream's package-level `codexCreds` unchanged and add fork behaviour in the executor method wrapper; the fork's Codex gateway lives at `upstream.codex.base-url` (alias `codex.base-url`).
- The same-credential transient retry must skip request-fault errors (`isApplyPatchToolArgumentFault`), otherwise one failed request publishes several failure usage records. Transient failures that survive those retries are cooldown evidence; do not re-add a blanket "transient errors never cool" rule.
- An explicit `model_not_found` on a subscription credential (`codex`, `claude`) uses the 30-minute account-scoped probe window; API-key providers follow upstream's transient/model-support classification.
- v8 management handlers own a `CloneForRuntime` copy of the config, so handler-side edits appear on `h.cfg` and in the persisted `config.yaml`, not on the caller's struct; tests must assert accordingly.
- End-to-end compatibility after a merge is verified with `scripts/windows/smoke-account-pool-exemption.ps1` (real requests through CPA `18399` and CPA-Manager `18318`) plus `scripts/windows/smoke-panel-v8-endpoints.ps1` (panel-facing v0/v8 endpoint matrix and the fork-only quota round-trip). Run both before declaring a merge healthy.
- v8 dropped `PUT /v0/management/config` (JSON) and the whole `internal/api/modules/amp` surface (`/ampcode*`). The panel does not use the former (it saves through granular endpoints and `PUT /v0/management/config.yaml`, which still accepts legacy flat YAML); the latter leaves the panel's Amp page without a backend.
- Two upstream tests fail on pristine `upstream/main` and are not merge regressions: `internal/translator/claude/openai/responses` `TestToolSearchDisabledMatchesDevGolden` and `sdk/api/handlers/openai` `TestResponsesInterruptStopsHTTPUpstream`.

## Account-pool change record
- Ordinary account-pool UI and navigation deliberately have no exemption toggle or dedicated borrowing instructions. The user-approved hidden workspace is `/management.html#/plugin-pages/enterprise-access-audit/0/account-pool-exemptions`; Manager forwards only this allowlisted view to the existing plugin resource/authentication bridge. Retain backend `Binding.crossPoolExempt` and full-snapshot API; ordinary UI publications preserve flags. No private whitelist, hidden gesture, or new permission system. Upgrade both DLL and embedded Manager; verify via `scripts/windows/verify-account-pool-hidden-workspace.ps1`, with staged owned Manager at `build/cpa-manager-account-pool.exe`.
- Targeted cross-pool exemption lives entirely in `plugins/enterprise-access-audit`: `Binding.crossPoolExempt` defaults off. Ordinary users retain primary-pool isolation; exempt new sessions prefer primary capacity and may borrow only sustained spare capacity from enabled existing department pools. Active sessions never migrate on failure, cooling, saturation, exemption toggles, or pool-policy changes.
- Borrowing observes 60 complete seconds (mean execution concurrency <= 40% of configured limit), requires 15 seconds without queue/full-execution pressure, and leaves one slot at atomic selection (`active + reserved + this pick <= limit - 1`). Do not add ongoing protected-slot admission. Missing session identity or unconfigured/unlimited concurrency excludes cross-pool borrowing. In-flight work prevents session idle expiry.
- `reserved` is a short-lived selection-to-admission counter, not department-protected capacity. Consume only the matching host execution-ID pick once per admission entry, not once per queue wake; expired/unselected admissions must not consume peers' picks. Carry the host ID with the existing before/after-auth interceptor header contract, overwrite client input, and strip it from returned updates as well as `ClearHeaders` before upstream (the host clears first, then merges updates). Duplicate same-auth admission consumes a fresh own repick without recounting execution. Missing correlation excludes borrowing; release unused provider-layer picks. API fallback admission also holds the original OAuth session's lifetime reference. Diagnostics include `averageActive`, `borrowReady`, and `borrowable`. Keep execution telemetry while account-pool is disabled without enforcing saved limits, so re-enabling cannot mistake still-running off-period traffic for idle capacity.
- Plugin scheduler fail-closed (`policy_unavailable`) must stay diagnosable: `Host.failClosedScheduler` logs a warn with `provider`/`branch`/`plugin_id`/`detail`, the error message appends the branch detail, and `publishSchedulerPickFailure` publishes an auth-less `executor_type: scheduler` failure record so the class appears in request monitoring (executors are never reached, so nothing else would record it). Verified end-to-end on a scratch instance: a live exclusive claim with no serving record yields 503 `policy_unavailable: ... no serving scheduler record for owner <id>` plus one failed usage-queue row carrying `error_code=policy_unavailable`.
- Focused verification: `scripts/windows/verify-account-pool-exemption.ps1 -Phase Full -Race`; synthetic Manager/CPA smoke: `scripts/windows/smoke-account-pool-exemption.ps1` (`-KeepAlive` for browser validation). While that owned fixture is alive, `scripts/windows/probe-account-pool-off-observation.ps1` validates disabled/reenabled physical occupancy. The verifier also executes `scripts/test_account_pool_exemption_ui.cjs` to catch formatter runtime-reference errors that syntax checks alone miss. Scratch uses `build_tmp/account-pool-exemption`, ports 18318/18399/18400, generated synthetic credentials, and never touches production processes. Stage the compiled DLL under `build/` before delivery.

## ModelTrace change record
- Native ModelTrace routes reuse management authentication and fixed AuthID execution; do not route evaluations through `/api-call` or add post-connect timeouts/retries.
- Keep original bank/context bytes and both embedded MIT notices. `.gitattributes` marks `internal/modeltrace/data/**/*.json` binary because BankRevision hashes the raw JSON; verify index/worktree bytes match when staging data. Attribution confidence is relative to the candidate bank, not a measured downgrade probability; the 0.8 display threshold is uncalibrated.
- Product verdict: only exact `gpt-5.6-luna` is suspicious; other candidates (including `gpt-6-luna`) use `consistent` (no degradation detected). Target/fingerprint matching is independent. Reclassify saved evidence at load without model execution; keep the three-valid-challenge/0.8 confidence gate.
- Persist running markers before execution to `modeltrace-state.json` next to config, not under auth-dir. Restart marks interrupted work without reissuing it; keep 20 records per account-bound credential.
- Codex SDK chunks are complete SSE lines without delimiters. ModelTrace explicitly decodes SDK lines; HTTP byte fragments use a separate accumulation contract. Never infer framing from event/comment prefixes; frame limits count raw whitespace. Cover both contracts and their error/limit paths.
- Management ModelTrace sets the SDK-only `WithManagementCredentialProbe` context; exclusive scheduling recognizes it only with internal source plus one exact pinned candidate. It bypasses employee pool binding, not plugin admission/concurrency/window limits. Ordinary missing caller identity stays fail-closed; do not use a fabricated employee or management API key. CPA and enterprise-audit plugin must be upgraded together for this contract.
- Focused verification: `scripts/windows/verify-modeltrace.ps1` (`-Race` optional); logs in `logs/`, incremental binary in `build/`. Never use production credentials/ports for unit verification.
- 2026-10-22 release: CPA **v8.0.42** (plugin-scheduler fail-closed diagnostics) pairs with manager `v1.26.7` at pinned commit `ef49105d3995c01560ff1e90179f08438af2e04a` (`MANAGEMENT_CENTER_REF`). The fork continues upstream's version line, so tags must stay above upstream's latest (`v8.0.35` at the 2026-10-20 merge) to avoid colliding with fetched upstream tags. Verified after publication: `manifest.json` (`bundleVersion`/`cpaVersion` 8.0.42, `managerVersion` v1.26.7, pinned commit), `checksums.txt` (10 CPA archives) and `suite-checksums.txt` (6 Suite archives) match GitHub's published asset digests; the packaged `cli-proxy-api.exe` reports `X-Cpa-Version: 8.0.42`, `X-Cpa-Commit: d3a23df2`, and reproduces the fail-closed 503 plus its `executor_type: scheduler` usage row. The `docker-image` workflow fails with "Username and password required" because DockerHub secrets are absent in the fork — pre-existing, unrelated to code.
- 2026-10-08 release: CPA v7.3.34 pairs with manager v1.24.6 at immutable commit `c597d0bec50486ac7d5910252c967591c62db56f` in `.github/workflows/release.yaml`. Verify the published Suite manifest's manager version/commit and six archive checksums before reporting release success.

## Suite launcher/update change record
- Unified package launchers start `cli-proxy-api` with default `config.yaml` (or `--config <path>`) before CPA-Manager with `--no-start-cpa`; `stop.*` acts only on identity-verified PIDs from `.suite-runtime.json`.
- CPA-Manager's System page is manual check/download only. `cpa-updater` is the automatic installer; it verifies the canonical release checksum, transactionally replaces program assets, preserves user config/auth/database/log/plugin settings, restarts with recorded arguments, and rolls back on health failure.
- `release/package-unified.sh` writes `suite-version.json`; the release workflow must embed the matching Manager version and pin the updated Management Center source to an immutable full commit before joint release.
- Endpoints under the `/v0/management` base URL are deprecated and no longer maintained upstream. For any feature changes, do not modify endpoints under `/v0/management` unless necessary to fix compilation errors. This fork keeps the frozen `/v0` surface for compatibility (decided during the v8 merge) and adds fork-only endpoints there; new features go under `/v8/management`.
