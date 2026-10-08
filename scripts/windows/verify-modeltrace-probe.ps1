param([switch]$Race)
$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
New-Item -ItemType Directory -Force (Join-Path $root 'logs') | Out-Null
Push-Location $root
try {
    $goArgs = @('test')
    if ($Race) { $goArgs += '-race' }
    $goArgs += @('./internal/modeltrace', './internal/pluginhost', './sdk/api/handlers', '-run', '(ModelTrace|ManagementProbe|ExclusiveScheduler|PinnedAuth|PinsExactAuth|UsesForcedProvider)', '-count=1', '-timeout=120s')
    & go @goArgs 2>&1 | Tee-Object -FilePath logs/modeltrace-probe.log
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Set-Location (Join-Path $root 'plugins\enterprise-access-audit\go')
    $goArgs = @('test')
    if ($Race) { $goArgs += '-race' }
    $goArgs += @('./internal/accountpool', '-count=1', '-timeout=120s')
    & go @goArgs 2>&1 | Tee-Object -FilePath (Join-Path $root 'logs\modeltrace-probe-pool.log')
    exit $LASTEXITCODE
} finally { Pop-Location }
