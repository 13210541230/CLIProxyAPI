# Verifies the upstream v8 merge: build, vet, focused package tests, plugin module.
# Two upstream tests and the Windows unsafe.Pointer vet notes already fail on a
# pristine upstream/main checkout, so they are filtered as documented pre-existing
# issues; anything else still fails the run. Do not leave a second worktree (for
# example a pristine upstream checkout under build_tmp) inside the repository while
# running: internal/util TestInPlaceByteWritesAreReviewed scans the whole tree.
param([string]$LogPath)
$ErrorActionPreference = 'Continue'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$log = if ($LogPath) { $LogPath } else { Join-Path $repo 'logs\verify-upstream-v8-merge.log' }
Set-Location $repo
if (Test-Path $log) { Remove-Item $log -Force }
$failed = @()
# Documented pre-existing upstream conditions (reproduced on a pristine upstream
# worktree): vet notes about unsafe.Pointer in the DLL loader, one openai handler
# test, and one translator golden test.
$knownPatterns = @{
  'test-sdk-api' = @('TestResponsesInterruptStopsHTTPUpstream', 'openai_responses_interrupt_test.go')
  'test-translator' = @('TestToolSearchDisabledMatchesDevGolden', 'claude_openai-responses_tool_search_disabled_test.go')
  'test-core' = @('TestToolSearchDisabledMatchesDevGolden', 'claude_openai-responses_tool_search_disabled_test.go')
}
function Write-Log([string]$text) { $text | Out-File -FilePath $log -Append -Encoding utf8 }
function Test-Known([string]$name, [string]$line) {
  if ($name -eq 'go-vet') {
    $normalized = $line.Replace('\', '/')
    return $normalized -match 'internal/pluginhost/loader_windows(_test)?\.go:\d+:\d+: possible misuse of unsafe\.Pointer'
  }
  foreach ($pattern in $knownPatterns[$name]) { if ($line -like "*$pattern*") { return $true } }
  return $false
}
function Step([string]$name, [scriptblock]$block) {
  Write-Log "=== $name"
  $out = & $block 2>&1 | Out-String
  $code = $LASTEXITCODE
  $unexpected = @()
  $knownSeen = $false
  foreach ($line in ($out -split "`r?`n")) {
    if ($line.Trim() -eq '') { continue }
    if (Test-Known $name $line) { $knownSeen = $true; continue }
    # Build/test summaries belong to the exit code check, not to failure detection.
    if ($line -match '^(FAIL|ok)\s') { continue }
    if ($line -match '^\s*---\s*FAIL:') { $unexpected += $line; continue }
    if ($code -ne 0 -and $line -match '\.go:\d+:') { $unexpected += $line; continue }
    if ($code -ne 0 -and $line -match '^\s+---\s*FAIL:') { $unexpected += $line; continue }
    if ($code -ne 0 -and $line -match 'panic:|undefined:|cannot use|build failed') { $unexpected += $line; continue }
  }
  if ($code -ne 0 -and -not $knownSeen -and $unexpected.Count -eq 0) {
    $unexpected += "command failed with exit code $code without a recognized baseline failure"
  }
  if ($out) { Write-Log $out.TrimEnd() }
  Write-Log "=== $name exit=$code unexpected=$($unexpected.Count)"
  if ($unexpected.Count -gt 0) {
    $script:failed += $name
    foreach ($line in $unexpected) { Write-Log "!!! $name unexpected: $line" }
    return
  }
  if ($code -ne 0) { Write-Log "=== $name known pre-existing failures only" }
}
Step 'go-build-all'    { go build ./... }
Step 'go-vet'          { go vet ./... }
Step 'test-auth'       { go test ./sdk/cliproxy/auth/... }
Step 'test-executor'   { go test ./internal/runtime/executor/... }
Step 'test-config'     { go test ./internal/config/... }
Step 'test-pluginhost' { go test ./internal/pluginhost/... }
Step 'test-api'        { go test ./internal/api/... }
Step 'test-redisqueue' { go test ./internal/redisqueue/... }
Step 'test-sdk-api'    { go test ./sdk/api/... }
Step 'test-cliproxy'   { go test ./sdk/cliproxy/... }
Step 'test-translator' { go test ./internal/translator/... }
Step 'test-core'       { go test ./internal/... -count=1 }
Step 'plugin-build'    { Push-Location plugins\enterprise-access-audit\go; try { go build ./... } finally { Pop-Location } }
Step 'plugin-test'     { Push-Location plugins\enterprise-access-audit\go; try { go test ./... } finally { Pop-Location } }
if ($failed.Count -gt 0) {
  Write-Log "=== FAILED: $($failed -join ', ')"
  exit 1
}
Write-Log '=== COMPLETED: no unexpected failures (documented baseline failures may be excluded)'
exit 0
