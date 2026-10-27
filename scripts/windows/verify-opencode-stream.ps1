param([switch]$Red, [switch]$Focus, [switch]$Race, [switch]$BuildOnly)
$ErrorActionPreference = 'Continue'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
New-Item -ItemType Directory -Force -Path (Join-Path $root 'logs'), (Join-Path $root 'build') | Out-Null
Push-Location $root
try {
    if ($Red -or $Focus) {
        $log = if ($Red) { 'logs/opencode-stream-red.log' } else { 'logs/opencode-stream-green.log' }
        go test ./internal/opencode -run '^TestOpenCodeStream' -count=1 -timeout=60s -v 2>&1 | Tee-Object -FilePath $log
        exit $LASTEXITCODE
    }
    if ($Race) {
        go test -race ./internal/opencode -run '^TestOpenCodeStream' -count=1 -timeout=90s -v 2>&1 | Tee-Object -FilePath logs/opencode-stream-race.log
        exit $LASTEXITCODE
    }
    if (-not $BuildOnly) {
        go test ./internal/opencode -count=1 -timeout=180s 2>&1 | Tee-Object -FilePath logs/opencode-stream-package.log
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
        go test ./sdk/api/handlers/claude ./sdk/api/handlers/openai ./sdk/translator -run '(Stream|SSE|Translate)' -count=1 -timeout=120s 2>&1 | Tee-Object -FilePath logs/opencode-stream-handlers.log
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
    go build -o build/cli-proxy-api.exe ./cmd/server 2>&1 | Tee-Object -FilePath logs/opencode-stream-build.log
    exit $LASTEXITCODE
} finally { Pop-Location }
