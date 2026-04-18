# OpenAI-Compatible Tool Calls Incident Report (Claude SSE Path)

## Summary

This report documents an intermittent streaming issue observed in the OpenAI-compatible path when consumed by Claude Code.

Observed symptoms:

- First turn can succeed, but later turns may end with empty output.
- Some runs show repeated `invalid tools` / invalid tool parameter behavior.
- Some runs appear to "stall" and then suddenly recover.

## Impact

- Tool-call reliability is unstable for a subset of OpenAI-compatible upstream models.
- User experience degrades into empty-finish or malformed tool-call outcomes.

## Root Cause

The issue is caused by upstream chunking behavior for `tool_calls` deltas:

- Certain models emit `arguments` fragments before stable `name`/`id` fragments.
- The translator/executor pipeline previously finalized too early on `finish_reason=tool_calls`.
- This produced either:
  - no consumable tool-use block in time (empty-finish), or
  - partially inferred/invalid tool metadata.

This is not primarily a transport disconnect issue; it is an event ordering/completeness issue at stream boundaries.

## Implemented Fixes

### 1) Translator: incremental tool args + single tool-use start

File: `internal/translator/openai/claude/openai_claude_response.go`

- Start `tool_use` block once (after required context is available).
- Emit `arguments` as incremental `input_json_delta` chunks.
- On stream finish (`finish`/`[DONE]`), emit only the remaining tail delta, then `content_block_stop`.

### 2) Translator: tool name inference hardening

File: `internal/translator/openai/claude/openai_claude_response.go`

- Added robust fallback inference chain for missing tool names:
  - single available tool
  - schema-based candidate matching
  - heuristic fallback

### 3) Executor: grace window before terminalization

File: `internal/runtime/executor/openai_compat_executor.go`

- When `finish_reason=tool_calls` arrives without usable `name/id`, hold for a short grace window (~800ms).
- Continue consuming potential late-arriving chunks before final terminal event handling.
- Suppress duplicate synthetic `[DONE]` injection when upstream already sent real `[DONE]`.

## Validation Notes

The above changes significantly improved stability in local reproduction:

- Empty-finish frequency reduced.
- Invalid tool-parameter bursts reduced.
- Multi-turn tool-call continuity improved.

## If Issue Reappears: Triage Checklist

1. Reproduce with the same model and prompt set in multi-turn mode.
2. Capture SSE sequence around first `finish_reason=tool_calls`.
3. Verify whether `name/id` arrives after initial `arguments` deltas.
4. Check whether terminalization happened before late `name/id` chunks.
5. Compare behavior with and without grace-window path.
6. Confirm no duplicate terminal markers (`[DONE]`) are emitted.

## Boundaries and Tradeoffs

- Waiting indefinitely is unsafe (can hang streams), so a bounded grace window is used.
- Inference is best-effort; upstream models that never provide coherent tool metadata can still fail.
- The fix prioritizes protocol continuity and practical compatibility over strict early-fail behavior.

## Current Status

- Stabilization patches are in place.
- Temporary local diagnostic logging changes were removed from code.
- Incident guidance is preserved in this document for future regression investigation.

