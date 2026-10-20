# Probes the panel-facing v8 management surface so a future upstream merge cannot
# silently drop endpoints the Management Center depends on. Requires a running CPA
# (any scratch instance); it only issues reads plus one reversible quota write.
[CmdletBinding()]
param(
  [string]$BaseUrl = 'http://127.0.0.1:18399',
  [Parameter(Mandatory = $true)][string]$ManagementKey
)
$ErrorActionPreference = 'Continue'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$log = Join-Path $repo 'logs\smoke-panel-v8-endpoints.log'
if (Test-Path $log) { Remove-Item $log -Force }
function Write-Log([string]$text) { $text | Out-File -FilePath $log -Append -Encoding utf8 }
$headers = @{ Authorization = "Bearer $ManagementKey" }
$failures = @()

# Endpoints the Management Center calls. PATCH-only routes are probed with their
# real method; everything else is a GET.
$expectations = @(
  @{ Path = '/v0/management/config';            Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/config.yaml';       Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/api-keys';          Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/auth-files';        Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/auth-files/fields'; Method = 'PATCH'; Expect = 400 },
  @{ Path = '/v0/management/codex-api-key';     Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/claude-api-key';    Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/gemini-api-key';    Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/vertex-api-key';    Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/xai-api-key';       Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/openai-compatibility'; Method = 'GET'; Expect = 200 },
  @{ Path = '/v0/management/plugins';           Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/oauth-excluded-models'; Method = 'GET'; Expect = 200 },
  @{ Path = '/v0/management/quota/config';      Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/routing/strategy';  Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/request-retry';     Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/request-log';       Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/debug';             Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/proxy-url';         Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/ws-auth';           Method = 'GET';  Expect = 200 },
  @{ Path = '/v0/management/request-error-logs'; Method = 'GET'; Expect = 200 },
  # v8 management surface: the fork keeps v0 frozen but must not lose v8 either.
  @{ Path = '/v8/management/config';            Method = 'GET';  Expect = 200 },
  @{ Path = '/v8/management/credentials';       Method = 'GET';  Expect = 200 },
  @{ Path = '/v8/management/plugins';           Method = 'GET';  Expect = 200 }
)

foreach ($entry in $expectations) {
  $status = $null
  try {
    $response = Invoke-WebRequest -Uri ($BaseUrl + $entry.Path) -Method $entry.Method -Headers $headers -TimeoutSec 8 -UseBasicParsing
    $status = [int]$response.StatusCode
  } catch {
    if ($_.Exception.Response -and $_.Exception.Response.StatusCode) { $status = [int]$_.Exception.Response.StatusCode }
  }
  $ok = ($status -eq $entry.Expect)
  Write-Log ("{0} {1,-6} {2,-45} expect={3} got={4}" -f $(if ($ok) { 'OK  ' } else { 'FAIL' }), $entry.Method, $entry.Path, $entry.Expect, $status)
  if (-not $ok) { $failures += "$($entry.Method) $($entry.Path) got=$status want=$($entry.Expect)" }
}

# Quota config round-trip: the fork-only section must survive a write and stay
# visible to the next read, otherwise spend-limit enforcement silently resets.
$quotaBody = '{"enabled":true,"db_path":"smoke-quota.db","mode":"cost","default":{"daily_cents":15000,"weekly_cents":50000}}'
try {
  $null = Invoke-WebRequest -Uri ($BaseUrl + '/v0/management/quota/config') -Method PUT -Headers $headers -ContentType 'application/json' -Body $quotaBody -TimeoutSec 8 -UseBasicParsing
  $readBack = (Invoke-WebRequest -Uri ($BaseUrl + '/v0/management/quota/config') -Headers $headers -TimeoutSec 8 -UseBasicParsing).Content
  if ($readBack -match '"daily_cents"\s*:\s*15000' -and $readBack -match 'smoke-quota.db') {
    Write-Log 'OK   quota round-trip preserved limits and db_path'
  } else {
    Write-Log "FAIL quota round-trip lost fields: $readBack"
    $failures += 'quota round-trip'
  }
} catch {
  Write-Log "FAIL quota round-trip exception: $($_.Exception.Message)"
  $failures += 'quota round-trip exception'
}

if ($failures.Count -gt 0) {
  Write-Log "=== FAILED: $($failures -join '; ')"
  exit 1
}
Write-Log '=== ALL GREEN'
exit 0
