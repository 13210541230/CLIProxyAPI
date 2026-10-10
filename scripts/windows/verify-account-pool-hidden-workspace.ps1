[CmdletBinding()]
param([ValidateSet('Test','Build','All')][string]$Phase = 'All')
$ErrorActionPreference = 'Continue'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$manager = (Resolve-Path (Join-Path $repo '..\Cli-Proxy-API-Management-Center')).Path
$logs = Join-Path $repo 'logs'
$build = Join-Path $repo 'build'
$scratch = Join-Path $repo 'build_tmp\account-pool-hidden-manager-ui'
New-Item -ItemType Directory -Force -Path $logs,$build | Out-Null
function Invoke-Logged([string]$Command, [string[]]$Arguments, [string]$Log) {
    & $Command @Arguments 2>&1 | Tee-Object -FilePath (Join-Path $logs $Log)
    if ($LASTEXITCODE -ne 0) { throw "$Command failed: $LASTEXITCODE" }
}
try {
    if ($Phase -in @('Test','All')) {
        Push-Location $repo
        try {
            Invoke-Logged 'node' @('scripts/test_account_pool_exemption_ui.cjs') 'account-pool-hidden-normal-ui.log'
            Invoke-Logged 'node' @('scripts/test_account_pool_hidden_workspace.cjs') 'account-pool-hidden-ui.log'
        } finally { Pop-Location }
        Push-Location $manager
        try {
            Invoke-Logged 'node' @('node_modules/vitest/vitest.mjs','run','src/features/plugins/pluginResources.test.ts') 'account-pool-hidden-manager-test.log'
        } finally { Pop-Location }
    }
    if ($Phase -in @('Build','All')) {
        New-Item -ItemType Directory -Force -Path $scratch | Out-Null
        Push-Location $manager
        try {
            Invoke-Logged 'node' @('node_modules/typescript/bin/tsc','--noEmit') 'account-pool-hidden-manager-types.log'
            Invoke-Logged 'node' @('node_modules/vite/bin/vite.js','build','--outDir',$scratch,'--emptyOutDir') 'account-pool-hidden-manager-frontend.log'
        } finally { Pop-Location }
        $html = [System.IO.File]::ReadAllText((Join-Path $scratch 'index.html')).Replace("`r`n","`n")
        $ownedHTML = Join-Path $build 'management-account-pool.html'
        [System.IO.File]::WriteAllText($ownedHTML,$html,[System.Text.UTF8Encoding]::new($false))
        $replace = @{}
        $replace[(Join-Path $manager 'usage-service\internal\httpapi\web\management.html')] = $ownedHTML
        $overlay = Join-Path $scratch 'overlay.json'
        [System.IO.File]::WriteAllText($overlay,(@{Replace=$replace} | ConvertTo-Json),[System.Text.UTF8Encoding]::new($false))
        $oldCGO = $env:CGO_ENABLED
        $env:CGO_ENABLED = '0'
        Push-Location (Join-Path $manager 'usage-service')
        try {
            Invoke-Logged 'go' @('build','-overlay',$overlay,'-o',(Join-Path $build 'cpa-manager-account-pool.exe'),'./cmd/cpa-manager') 'account-pool-hidden-manager-build.log'
        } finally { Pop-Location; $env:CGO_ENABLED=$oldCGO }
        Write-Host "Staged embedded Manager: $(Join-Path $build 'cpa-manager-account-pool.exe')"
        Write-Host "Embedded HTML SHA256: $((Get-FileHash $ownedHTML -Algorithm SHA256).Hash)"
    }
    exit 0
} catch { Write-Host $_; exit 1 }
finally { if (Test-Path $scratch) { Remove-Item -Recurse -Force $scratch } }
