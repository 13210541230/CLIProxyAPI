# Check that the merge verifier fails for visible and silent command errors.
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$scratch = Join-Path $repo ("build_tmp\verify-upstream-selftest-" + [guid]::NewGuid().ToString('N'))
$fakeDir = Join-Path $scratch 'bin'
New-Item -ItemType Directory -Force -Path $fakeDir | Out-Null
@'
@echo off
if "%VERIFY_TEST_MODE%"=="known" goto known
if "%VERIFY_TEST_MODE%"=="mixed" goto mixed
if "%VERIFY_TEST_MODE%"=="cross" goto cross
if "%VERIFY_TEST_MODE%"=="vet-known" goto vetknown
if "%VERIFY_TEST_MODE%"=="vet-new" goto vetnew
if "%1"=="build" (
  if "%VERIFY_TEST_MODE%"=="message" echo synthetic.go:1: build failed 1>&2
  exit /b 7
)
exit /b 0
:known
if "%1"=="test" if "%2"=="./internal/translator/..." (
  echo --- FAIL: TestToolSearchDisabledMatchesDevGolden
  exit /b 1
)
exit /b 0
:mixed
if "%1"=="test" if "%2"=="./internal/translator/..." (
  echo --- FAIL: TestToolSearchDisabledMatchesDevGolden
  echo --- FAIL: TestUnexpectedFailure
  exit /b 1
)
exit /b 0
:cross
if "%1"=="test" if "%2"=="./internal/config/..." (
  echo --- FAIL: TestToolSearchDisabledMatchesDevGolden
  exit /b 1
)
exit /b 0
:vetknown
if "%1"=="vet" (
  echo internal\pluginhost\loader_windows.go:166:31: possible misuse of unsafe.Pointer 1>&2
  exit /b 1
)
exit /b 0
:vetnew
if "%1"=="vet" (
  echo internal\config\new.go:1:2: possible misuse of unsafe.Pointer 1>&2
  exit /b 1
)
exit /b 0
'@ | Set-Content -Encoding ascii -Path (Join-Path $fakeDir 'go.cmd')
$oldPath = $env:PATH
Push-Location $repo
try {
  $env:PATH = "$fakeDir;$oldPath"
  foreach ($mode in @('message', 'silent', 'mixed', 'known', 'cross', 'vet-known', 'vet-new')) {
    $env:VERIFY_TEST_MODE = $mode
    $log = Join-Path $scratch "$mode.log"
    & ./scripts/windows/verify-upstream-v8-merge.ps1 -LogPath $log
    $result = $LASTEXITCODE
    $expected = if ($mode -in @('known', 'vet-known')) { 0 } else { 1 }
    if ($result -ne $expected) { throw "Expected exit $expected for $mode, got $result" }
    if ($expected -eq 1 -and -not (Select-String -Path $log -SimpleMatch '=== FAILED:' -Quiet)) { throw "Missing FAILED record for $mode" }
    Write-Output "PASS $mode returned exit $expected"
  }
} finally {
  Pop-Location
  $env:PATH = $oldPath
  Remove-Item Env:VERIFY_TEST_MODE -ErrorAction SilentlyContinue
  Remove-Item $scratch -Recurse -Force -ErrorAction SilentlyContinue
}
exit 0
