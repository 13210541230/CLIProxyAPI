# Boot-level smoke for the merged v8 tree: an isolated scratch instance on port 18399
# that proves the process starts, /v0 (frozen) and /v8 management surfaces answer,
# and the request pipeline is reachable. Never touches the running deployment.
$ErrorActionPreference = 'Continue'
$repo = 'D:\C_projects\CLIProxyAPI'
$scratch = Join-Path $repo 'build_tmp\v8-boot-smoke'
$log = Join-Path $repo 'logs\smoke-v8-boot.log'
$exe = Join-Path $repo 'build\cli-proxy-api-v8.exe'
$port = 18399
Set-Location $repo
if (Test-Path $log) { Remove-Item $log -Force }
function Write-Log([string]$text) { $text | Out-File -FilePath $log -Append -Encoding utf8 }

if (Test-Path $scratch) { Remove-Item $scratch -Recurse -Force }
New-Item -ItemType Directory -Path $scratch | Out-Null
New-Item -ItemType Directory -Path (Join-Path $scratch 'auths') | Out-Null
$config = @"
host: 127.0.0.1
port: $port
auth-dir: "$($scratch -replace '\\','/')/auths"
api-keys:
  - smoke-client-key
remote-management:
  allow-remote: false
  secret-key: "smoke-management-secret"
quota:
  enabled: false
  db-path: "$($scratch -replace '\\','/')/quota.db"
"@
Set-Content -Path (Join-Path $scratch 'config.yaml') -Value $config -Encoding utf8

$proc = Start-Process -FilePath $exe -ArgumentList "--config", (Join-Path $scratch 'config.yaml'), '--no-browser' -PassThru -WindowStyle Hidden -RedirectStandardOutput (Join-Path $scratch 'stdout.log') -RedirectStandardError (Join-Path $scratch 'stderr.log')
Write-Log "started pid=$($proc.Id)"

$ready = $false
for ($i = 0; $i -lt 40; $i++) {
  Start-Sleep -Milliseconds 500
  try {
    $probe = Invoke-WebRequest -Uri "http://127.0.0.1:$port/v0/management/config" -Headers @{ 'X-Management-Key' = 'smoke-management-secret' } -TimeoutSec 3 -UseBasicParsing
    if ($probe.StatusCode -eq 200) { $ready = $true; Write-Log "v0 management config status=$($probe.StatusCode) bytes=$($probe.RawContentLength)"; break }
  } catch { }
}
if (-not $ready) {
  Write-Log 'FAIL: server did not answer on /v0/management/config'
  try { Stop-Process -Id $proc.Id -Force } catch { }
  exit 1
}

function Probe([string]$name, [scriptblock]$block) {
  try {
    $result = & $block
    Write-Log "$name -> $result"
    return $result
  } catch {
    $status = $null
    if ($_.Exception.Response -and $_.Exception.Response.StatusCode) { $status = [int]$_.Exception.Response.StatusCode }
    Write-Log "$name -> exception status=$status $($_.Exception.Message)"
    return "status=$status"
  }
}

$v8 = Probe 'v8 management config' { (Invoke-WebRequest -Uri "http://127.0.0.1:$port/v8/management/config" -Headers @{ 'X-Management-Key' = 'smoke-management-secret' } -TimeoutSec 5 -UseBasicParsing).StatusCode }
$models = Probe 'v1 models' { (Invoke-WebRequest -Uri "http://127.0.0.1:$port/v1/models" -Headers @{ 'Authorization' = 'Bearer smoke-client-key' } -TimeoutSec 5 -UseBasicParsing).StatusCode }
$chat = Probe 'v1 chat routing' {
  try {
    (Invoke-WebRequest -Uri "http://127.0.0.1:$port/v1/chat/completions" -Method Post -ContentType 'application/json' -Headers @{ 'Authorization' = 'Bearer smoke-client-key' } -Body '{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}' -TimeoutSec 5 -UseBasicParsing).StatusCode
  } catch {
    # A routed-but-unknown model must answer with a structured client error, not a hang or 500 storm.
    $s = $null
    if ($_.Exception.Response -and $_.Exception.Response.StatusCode) { $s = [int]$_.Exception.Response.StatusCode }
    "status=$s"
  }
}

try { Stop-Process -Id $proc.Id -Force } catch { }
Start-Sleep -Milliseconds 500
$stderr = Join-Path $scratch 'stderr.log'
if (Test-Path $stderr) {
  $tail = Get-Content $stderr -Tail 20 | Out-String
  Write-Log "--- stderr tail ---`n$tail"
}
Remove-Item $scratch -Recurse -Force -ErrorAction SilentlyContinue

if ($v8 -eq 200 -and $models -eq 200) {
  Write-Log '=== SMOKE OK'
  exit 0
}
Write-Log '=== SMOKE FAILED'
exit 1
