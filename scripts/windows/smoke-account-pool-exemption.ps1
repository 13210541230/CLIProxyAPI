[CmdletBinding()]
param([switch]$KeepAlive, [string]$ManagerBinary)
$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$logsDir = Join-Path $repoRoot 'logs'
New-Item -ItemType Directory -Force -Path $logsDir | Out-Null
Push-Location $repoRoot
try {
    $arguments = @('-u', 'scripts/account_pool_exemption_smoke.py')
    if ($KeepAlive) { $arguments += '--keep-alive' }
    if ($ManagerBinary) { $arguments += @('--manager-binary', (Resolve-Path $ManagerBinary).Path) }
    & python @arguments 2>&1 | Tee-Object -FilePath (Join-Path $logsDir 'account-pool-exemption-smoke.log')
    exit $LASTEXITCODE
} finally { Pop-Location }
