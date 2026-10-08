param([switch]$Red, [switch]$ApiRed, [switch]$Race)
$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
New-Item -ItemType Directory -Force -Path (Join-Path $root 'logs'), (Join-Path $root 'build') | Out-Null
Push-Location $root
try {
    if ($Red) {
        go test ./internal/modeltrace -count=1 2>&1 | Tee-Object -FilePath logs/modeltrace-red.log
        exit $LASTEXITCODE
    }
    if ($ApiRed) {
        go test ./internal/api/handlers/management ./internal/api -run ModelTrace -count=1 2>&1 | Tee-Object -FilePath logs/modeltrace-api-red.log
        exit $LASTEXITCODE
    }
    if ($Race) {
        go test -race ./internal/modeltrace ./internal/api/handlers/management ./internal/api -run '(ModelTrace|CodexPrefix|CodexTurn)' -count=1 -timeout=120s -v 2>&1 | Tee-Object -FilePath logs/modeltrace-race.log
        exit $LASTEXITCODE
    }
    go test ./internal/modeltrace -count=1 -timeout=90s -v 2>&1 | Tee-Object -FilePath logs/modeltrace-package.log
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go test ./internal/api/handlers/management -run ModelTrace -count=1 -timeout=90s -v 2>&1 | Tee-Object -FilePath logs/modeltrace-management.log
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go test ./internal/api -run ModelTrace -count=1 -timeout=90s -v 2>&1 | Tee-Object -FilePath logs/modeltrace-routes.log
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go test ./sdk/api/handlers -run '(AuthID|PinnedAuth|PinsExactAuth|UsesForcedProvider)' -count=1 -timeout=90s -v 2>&1 | Tee-Object -FilePath logs/modeltrace-pinning.log
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    go build -o build/cpa-modeltrace-verify.exe ./cmd/server 2>&1 | Tee-Object -FilePath logs/modeltrace-build.log
    $code = $LASTEXITCODE
    Write-Output "BUILD_EXIT_CODE=$code"
    exit $code
} finally { Pop-Location }
