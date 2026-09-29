$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$logsDir = Join-Path $repoRoot 'logs'
$buildDir = Join-Path $repoRoot 'build'
New-Item -ItemType Directory -Force -Path $logsDir, $buildDir | Out-Null

Push-Location $repoRoot
try {
    go test ./... *> (Join-Path $logsDir 'bps-root-tests.log')
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    Push-Location (Join-Path $repoRoot 'plugins\enterprise-access-audit\go')
    try {
        go test -race ./... -count=1 -timeout=180s *> (Join-Path $logsDir 'bps-enterprise-race-tests.log')
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        go build -buildmode=c-shared -o (Join-Path $buildDir 'enterprise-access-audit-bps-e2e.dll') . *> (Join-Path $logsDir 'bps-plugin-build.log')
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    finally {
        Pop-Location
    }

    go build -o (Join-Path $buildDir 'cli-proxy-api-bps-e2e.exe') ./cmd/server *> (Join-Path $logsDir 'bps-cpa-build.log')
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
