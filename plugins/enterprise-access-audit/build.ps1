[CmdletBinding()]
param(
    [string]$OutputDirectory = ''
)

$ErrorActionPreference = 'Stop'
$moduleDirectory = Join-Path $PSScriptRoot 'go'
$repositoryRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')

function Invoke-Go([string[]]$Arguments) {
    Push-Location $moduleDirectory
    try {
        & go @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

$goos = (& go env GOOS).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goos)) {
    throw 'go env GOOS failed'
}
$goarch = (& go env GOARCH).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goarch)) {
    throw 'go env GOARCH failed'
}
$extension = switch ($goos) {
    'windows' { '.dll'; break }
    'darwin' { '.dylib'; break }
    default { '.so' }
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repositoryRoot "plugins\$goos\$goarch"
} elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path (Get-Location) $OutputDirectory
}
$OutputDirectory = (Resolve-Path (New-Item -ItemType Directory -Force -Path $OutputDirectory)).Path

Invoke-Go @('test', './...')
Invoke-Go @('test', '-race', './...')
Invoke-Go @('build', './...')

$artifactPath = Join-Path $OutputDirectory "enterprise-access-audit$extension"
Invoke-Go @('build', '-buildmode=c-shared', '-o', $artifactPath, '.')
$headerPath = [System.IO.Path]::ChangeExtension($artifactPath, '.h')
if (Test-Path $headerPath) {
    Remove-Item -Force $headerPath
}
Write-Output "Built enterprise-access-audit for ${goos}/${goarch}: $artifactPath"
