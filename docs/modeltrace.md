# Native Codex ModelTrace

ModelTrace is a management-authenticated, asynchronous attribution feature for individual Codex credentials. It does not require a plugin or Usage Service and never changes credential priority, availability, quotas or policy.

## Interpretation and cost

This is candidate-bank model attribution, **not proof of model identity or a measured downgrade probability**. Three valid challenges and a relative attribution probability of at least 0.8 are required for a decisive product verdict. Only the exact `gpt-5.6-luna` fingerprint is `suspected` (suspected degradation); all other candidates, including the distinct `gpt-6-luna`, are `consistent` (no degradation detected). Comparison with the selected target is independent: a different inferred fingerprint does not by itself imply degradation. The threshold is a conservative display rejection rule; it has **not been calibrated against production data**. Partial runs, low-confidence results, refusals and errors are `inconclusive`. Lifecycle and verdict are separate: a fully collected low-confidence run can be `completed` and still `inconclusive`.

A normal run is estimated at approximately **42,000 input tokens** for the three full Codex-context challenges, excluding output and existing CPA retries. This estimate is not usage measurement. Records sum only actual usage returned by the upstream stream and report elapsed duration. No real paid model requests are part of unit verification.

## API (relative to `/v0/management`)

- `GET /auth-files/modeltrace`: bank revision, bank models `{id,display_name}`, and a single batched `states` map keyed by `auth_index`; optional `storage_error`. States contain running progress and latest summaries, without samples or rankings. The caller intersects bank models with the existing selected credential model catalog.
- `POST /auth-files/modeltrace`: `{auth_index,model}`; returns HTTP 202 `{run}`. Exactly three original randomized challenges execute in parallel. Only candidate-bank IDs present in the credential's registry are permitted.
- `DELETE /auth-files/modeltrace?auth_index=...`: `{cancelled:boolean}`.
- `GET /auth-files/modeltrace/record?auth_index=...&id=...`: bounded details for that credential. Omit `id` for latest.

Invalid requests/targets return 400; unknown credentials/records 404; duplicate, disabled or unavailable credentials 409; run capacity 429; unavailable execution or storage 503. Error bodies are `{error:string}`. All routes use existing management availability and authentication middleware.

## Execution boundary

Execution reuses `BaseAPIHandler.ExecuteModelStream`, `ForcedProvider=codex`, `AuthID`, and the original Codex protocol/context. Existing CPA accounting, selection constraints and hooks remain active. There is one run per credential and at most two distinct runs globally, hence at most six concurrent challenge requests; excess runs are rejected without a queue. No evaluator retries or post-connect network timeouts are added. Cancellation and `Server.Stop` cancel outstanding streams.

When enterprise account-pool exclusive scheduling is enabled, management detection uses a typed internal credential-probe context rather than impersonating a business API key. The host accepts this scope only from an internal SDK execution whose candidates have already been narrowed to the pinned AuthID. Employee pool bindings do not reroute the diagnostic, but the plugin's ordinary account admission, concurrency and request-window limits still apply. Normal business requests without canonical caller identity remain rejected; clients cannot enable this scope through HTTP headers or JSON. Upgrading this integration requires both CPA and the bundled enterprise-audit plugin.

SSE deltas and complete response output are supported without duplicate accumulation. SDK chunks are explicitly complete SSE lines without delimiters; HTTP byte fragments use a separate accumulating decoder. Event/comment prefixes do not determine the framing contract, and limits count raw whitespace. Limits are 64,000 text bytes per challenge, 256 KiB per pending SSE frame and 2 MiB per entire stream. Incomplete/failed streams are not accepted as valid samples. Errors retain a static execution stage and HTTP status where available (for example, `model execution rejected (HTTP 401)`), rather than persisting potentially secret-bearing upstream error bodies, tokens or prompts. Existing generic failure records cannot recover a previously discarded status; reading them never reruns the model.

## Durable state

The fixed `modeltrace-state.json` is next to the selected config file, outside `auth-dir`. Initial `running` markers are synced and atomically renamed before paid work can start. Final results replace the marker; each credential retains at most 20 records. Restart converts running markers to `interrupted`, never reissues work, and persists that conversion. Stored verdicts are also reclassified with the current Luna rule using existing evidence only; original target, fingerprint, samples and measured usage are preserved.

Record association uses a SHA-256 digest of provider, AuthID and a non-secret account identifier (account ID preferred; email fallback). Unknown identity cannot start a run. A replacement account under the same auth filename/index does not inherit results. Credential tokens and auth metadata are neither returned in this API nor written to the state file.

Load/save failures appear through `storage_error`, and a final save failure also marks the in-memory record's `storage_error`. New runs remain blocked after a storage failure; fix the storage path/state and restart. Do not treat an in-memory result with a storage error as durable. A restart after failed final persistence may reveal the last durable marker as interrupted.

## Sources and licenses

The narrow scoring, challenge generator, Codex context/request builder and golden fixtures were ported from `cpa-plugin-codex-candy-eval` at `876d1ab480ca297b82367f09392dbbcb785e86c8`. The original ModelTrace scoring/challenges were pinned there to `xqy2006/ModelTrace` commit `df3a0f9d3e054c0dc02d6d586686db8daf8fa7c8`.

Both MIT notices are preserved in source and embedded binaries:

- `internal/modeltrace/data/PLUGIN-LICENSE`: Copyright (c) 2026 Hao Wang.
- `internal/modeltrace/data/modeltrace/LICENSE`: Copyright (c) 2026 xqy2006.

The copied bank/context bytes are unchanged. Bank revision is the original bank's SHA-256 digest; candidate changes require refitting the bank, not adding arbitrary aliases. Golden tests verify original 1/2/3-output probabilities, rankings, scores, similarities and diagnostics with tolerance `1e-10`.

## Verification

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/windows/verify-modeltrace.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/windows/verify-modeltrace.ps1 -Race
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/windows/verify-modeltrace-probe.ps1 -Race
```

The script overwrites bounded logs under `logs/`, runs package, management, routes and existing pinning tests, then incrementally builds `build/cpa-modeltrace-verify.exe`. No server is started and no credentials are loaded by this verification. Keep deployment/browser validation isolated from production.
