[CmdletBinding()]
param(
    [ValidateSet('Unit', 'Focus', 'Full', 'Build')][string]$Phase = 'Full',
    [switch]$Race
)
$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$logsDir = Join-Path $repoRoot 'logs'
$buildDir = Join-Path $repoRoot 'build'
$moduleDir = Join-Path $repoRoot 'plugins\enterprise-access-audit\go'
New-Item -ItemType Directory -Force -Path $logsDir, $buildDir | Out-Null
& node (Join-Path $repoRoot 'scripts\test_account_pool_exemption_ui.cjs') 2>&1 | Tee-Object -FilePath (Join-Path $logsDir 'account-pool-exemption-ui-tests.log')
if ($LASTEXITCODE -ne 0) { throw 'Embedded account-pool UI regression failed' }
function Invoke-LoggedGo([string[]]$Arguments, [string]$LogName) {
    $ErrorActionPreference = 'Continue'
    & go @Arguments 2>&1 | Tee-Object -FilePath (Join-Path $logsDir $LogName)
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
Push-Location $moduleDir
try {
    $files = @('main.go', 'main_pool_reservation_test.go', 'main_config_removal_test.go', 'internal/basispoints/auth.go', 'internal/basispoints/auth_gateway_test.go', 'internal/accountpool/policy.go', 'internal/accountpool/service.go', 'internal/accountpool/service_test.go', 'internal/accountpool/session.go', 'internal/accountpool/concurrency.go', 'internal/accountpool/borrowing.go', 'internal/accountpool/borrowing_test.go', 'internal/accountpool/borrowing_edges_test.go', 'internal/accountpool/borrowing_review_test.go', 'internal/accountpool/defect_fixes_test.go', 'internal/accountpool/concurrency_cleanup_test.go')
    & gofmt -w @files
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    if ($Phase -eq 'Unit') {
        Invoke-LoggedGo @('test', './internal/accountpool', '-count=1') 'account-pool-exemption-unit.log'
    } elseif ($Phase -eq 'Focus') {
        $testArgs = @('test', '.', './internal/accountpool', '-run', 'Test(PoolReservationID|DuplicateAdmission|ExpiredPick|FallbackInFlight|OrdinaryCaller|BorrowingObservesDisabled)', '-count=1')
        if ($Race) { $testArgs += '-race' }
        Invoke-LoggedGo $testArgs 'account-pool-exemption-focus.log'
    } elseif ($Phase -eq 'Full') {
        $testArgs = @('test', './...', '-count=1', '-timeout=180s')
        if ($Race) { $testArgs += '-race' }
        Invoke-LoggedGo $testArgs 'account-pool-exemption-tests.log'
    }
    if ($Phase -in @('Full', 'Build')) {
        Invoke-LoggedGo @('build', '-buildmode=c-shared', '-o', (Join-Path $buildDir 'enterprise-access-audit.dll'), '.') 'account-pool-exemption-plugin-build.log'
        $header = Join-Path $buildDir 'enterprise-access-audit.h'
        if (Test-Path $header) { Remove-Item $header -Force }
    }
} finally { Pop-Location }
if ($Phase -in @('Full', 'Build')) {
    Push-Location $repoRoot
    try {
        Invoke-LoggedGo @('build', '-o', (Join-Path $buildDir 'cli-proxy-api-e2e.exe'), './cmd/server') 'account-pool-exemption-cpa-build.log'
    } finally { Pop-Location }
}
exit 0
