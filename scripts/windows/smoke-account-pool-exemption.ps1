[CmdletBinding()]
param([switch]$KeepAlive)
$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$logsDir = Join-Path $repoRoot 'logs'
New-Item -ItemType Directory -Force -Path $logsDir | Out-Null
Push-Location $repoRoot
try {
    $arguments = @('-u', 'scripts/account_pool_exemption_smoke.py')
    if ($KeepAlive) { $arguments += '--keep-alive' }
    & python @arguments 2>&1 | Tee-Object -FilePath (Join-Path $logsDir 'account-pool-exemption-smoke.log')
    exit $LASTEXITCODE
} finally { Pop-Location }
