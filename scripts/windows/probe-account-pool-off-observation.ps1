[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Push-Location $repoRoot
try {
    & python -u scripts/probe_account_pool_off_observation.py 2>&1 | Tee-Object -FilePath logs/account-pool-exemption-off-observation.log
    exit $LASTEXITCODE
} finally { Pop-Location }
