#!/usr/bin/env bash
set -euo pipefail

VERSION=""
MANAGER_VERSION=""
TARGET_OS=""
TARGET_ARCH=""
SERVER_BIN=""
USAGE_BIN=""
UPDATER_BIN=""
MANAGEMENT_HTML=""
SERVER_PACKAGE_DIR=""
OUTPUT_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION="$2"
      shift 2
      ;;
    --manager-version)
      MANAGER_VERSION="$2"
      shift 2
      ;;
    --os)
      TARGET_OS="$2"
      shift 2
      ;;
    --arch)
      TARGET_ARCH="$2"
      shift 2
      ;;
    --server-bin)
      SERVER_BIN="$2"
      shift 2
      ;;
    --usage-bin)
      USAGE_BIN="$2"
      shift 2
      ;;
    --updater-bin)
      UPDATER_BIN="$2"
      shift 2
      ;;
    --management-html)
      MANAGEMENT_HTML="$2"
      shift 2
      ;;
    --server-package-dir)
      SERVER_PACKAGE_DIR="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

VERSION="${VERSION#v}"
if [[ -z "$VERSION" || -z "$MANAGER_VERSION" || -z "$TARGET_OS" || -z "$TARGET_ARCH" || -z "$SERVER_BIN" || -z "$OUTPUT_DIR" ]]; then
  echo "Missing required arguments" >&2
  exit 1
fi

if [[ ! -f "$SERVER_BIN" ]]; then
  echo "Server binary not found: $SERVER_BIN" >&2
  exit 1
fi

if [[ -z "$USAGE_BIN" ]]; then
  echo "CPA-Manager binary is required for a unified package" >&2
  exit 1
fi

if [[ -z "$UPDATER_BIN" ]]; then
  echo "Update helper binary is required for a unified package" >&2
  exit 1
fi

if [[ -n "$USAGE_BIN" && ! -f "$USAGE_BIN" ]]; then
  echo "CPA-Manager binary not found: $USAGE_BIN" >&2
  exit 1
fi

if [[ -n "$UPDATER_BIN" && ! -f "$UPDATER_BIN" ]]; then
  echo "Update helper binary not found: $UPDATER_BIN" >&2
  exit 1
fi

if [[ -n "$MANAGEMENT_HTML" && ! -f "$MANAGEMENT_HTML" ]]; then
  echo "Management HTML not found: $MANAGEMENT_HTML" >&2
  exit 1
fi

if [[ -n "$SERVER_PACKAGE_DIR" && ! -d "$SERVER_PACKAGE_DIR" ]]; then
  echo "CPA package directory not found: $SERVER_PACKAGE_DIR" >&2
  exit 1
fi

VERSION_NO_V="${VERSION#v}"
PKG_NAME="CLIProxyAPI-Suite_${VERSION_NO_V}_${TARGET_OS}_${TARGET_ARCH}"
PKG_DIR="${OUTPUT_DIR}/${PKG_NAME}"

rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR/static"

if [[ -n "$SERVER_PACKAGE_DIR" ]]; then
  # Only carry distributable CPA resources. Never copy an installation's
  # config, auth, cache, database, or log directories into an update bundle.
  for directory in static plugins; do
    if [[ -d "$SERVER_PACKAGE_DIR/$directory" ]]; then
      cp -R "$SERVER_PACKAGE_DIR/$directory" "$PKG_DIR/"
    fi
  done
fi

if [[ "$TARGET_OS" == "windows" ]]; then
  cp "$SERVER_BIN" "$PKG_DIR/cli-proxy-api.exe"
  cp "$USAGE_BIN" "$PKG_DIR/cpa-manager.exe"
  cp "$UPDATER_BIN" "$PKG_DIR/cpa-updater.exe"
  cat > "$PKG_DIR/start.bat" <<'BAT'
@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0start.ps1" %*
exit /b %ERRORLEVEL%
BAT
  cat > "$PKG_DIR/start.ps1" <<'PS1'
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root
$ConfigPath = Join-Path $Root 'config.yaml'
$UseDefaultConfig = $true
for ($Index = 0; $Index -lt $args.Count; $Index++) {
  if ($args[$Index] -eq '--config' -and $Index + 1 -lt $args.Count) {
    $ConfigPath = $args[$Index + 1]
    $UseDefaultConfig = $false
    $Index++
  } elseif ($args[$Index].StartsWith('--config=')) {
    $ConfigPath = $args[$Index].Substring('--config='.Length)
    $UseDefaultConfig = $false
  } else {
    throw "Unknown argument: $($args[$Index])"
  }
}
if (-not [IO.Path]::IsPathRooted($ConfigPath)) { $ConfigPath = Join-Path $Root $ConfigPath }
$ConfigPath = [IO.Path]::GetFullPath($ConfigPath)
if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
  if ($UseDefaultConfig) {
    $Template = Join-Path $Root 'config.example.yaml'
    if (-not (Test-Path -LiteralPath $Template -PathType Leaf)) { throw 'config.yaml and config.example.yaml are missing' }
    Copy-Item -LiteralPath $Template -Destination $ConfigPath
    Write-Host 'Created config.yaml from config.example.yaml; review it before production use.'
  } else {
    throw "Configuration file not found: $ConfigPath"
  }
}
$Updater = Join-Path $Root 'cpa-updater.exe'
$CPA = Join-Path $Root 'cli-proxy-api.exe'
$Manager = Join-Path $Root 'cpa-manager.exe'
foreach ($Path in @($Updater, $CPA, $Manager)) {
  if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Required suite file is missing: $Path" }
}
& $Updater --assert-stopped
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$Logs = Join-Path $Root 'logs'
New-Item -ItemType Directory -Path $Logs -Force | Out-Null
$CPAProcess = Start-Process -FilePath $CPA -ArgumentList @('--config', ('"{0}"' -f $ConfigPath)) -WorkingDirectory $Root -RedirectStandardOutput (Join-Path $Logs 'cli-proxy-api.stdout.log') -RedirectStandardError (Join-Path $Logs 'cli-proxy-api.stderr.log') -PassThru
& $Updater --record-runtime --runtime-cpa-pid $CPAProcess.Id --runtime-config $ConfigPath
if ($LASTEXITCODE -ne 0) {
  Stop-Process -Id $CPAProcess.Id -Force -ErrorAction SilentlyContinue
  exit $LASTEXITCODE
}
$ManagerProcess = Start-Process -FilePath $Manager -ArgumentList @('--no-start-cpa') -WorkingDirectory $Root -RedirectStandardOutput (Join-Path $Logs 'cpa-manager.stdout.log') -RedirectStandardError (Join-Path $Logs 'cpa-manager.stderr.log') -PassThru
& $Updater --record-runtime --runtime-cpa-pid $CPAProcess.Id --runtime-manager-pid $ManagerProcess.Id --runtime-config $ConfigPath
if ($LASTEXITCODE -ne 0) {
  Stop-Process -Id $ManagerProcess.Id -Force -ErrorAction SilentlyContinue
  & $Updater --stop
  exit $LASTEXITCODE
}
Write-Host "CLIProxyAPI started (PID $($CPAProcess.Id)); CPA-Manager started (PID $($ManagerProcess.Id))."
PS1
  cat > "$PKG_DIR/stop.bat" <<'BAT'
@echo off
setlocal
cd /d "%~dp0"
cpa-updater.exe --stop
exit /b %ERRORLEVEL%
BAT
else
  cp "$SERVER_BIN" "$PKG_DIR/cli-proxy-api"
  cp "$USAGE_BIN" "$PKG_DIR/cpa-manager"
  cp "$UPDATER_BIN" "$PKG_DIR/cpa-updater"
  chmod +x "$PKG_DIR/cli-proxy-api" "$PKG_DIR/cpa-manager" "$PKG_DIR/cpa-updater"
  cat > "$PKG_DIR/start.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"
CONFIG_PATH="$ROOT_DIR/config.yaml"
USE_DEFAULT_CONFIG=1
while (($#)); do
  case "$1" in
    --config)
      (($# >= 2)) || { echo "--config requires a path" >&2; exit 2; }
      CONFIG_PATH="$2"
      USE_DEFAULT_CONFIG=0
      shift 2
      ;;
    --config=*)
      CONFIG_PATH="${1#--config=}"
      USE_DEFAULT_CONFIG=0
      shift
      ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
done
if [[ "$CONFIG_PATH" != /* ]]; then CONFIG_PATH="$ROOT_DIR/$CONFIG_PATH"; fi
if [[ ! -f "$CONFIG_PATH" ]]; then
  if [[ "$USE_DEFAULT_CONFIG" == 1 && -f "$ROOT_DIR/config.example.yaml" ]]; then
    cp "$ROOT_DIR/config.example.yaml" "$CONFIG_PATH"
    echo "Created config.yaml from config.example.yaml; review it before production use."
  else
    echo "Configuration file not found: $CONFIG_PATH" >&2
    exit 1
  fi
fi
"$ROOT_DIR/cpa-updater" --assert-stopped
mkdir -p "$ROOT_DIR/logs"
nohup "$ROOT_DIR/cli-proxy-api" --config "$CONFIG_PATH" > "$ROOT_DIR/logs/cli-proxy-api.log" 2>&1 &
CPA_PID=$!
if ! "$ROOT_DIR/cpa-updater" --record-runtime --runtime-cpa-pid "$CPA_PID" --runtime-config "$CONFIG_PATH"; then
  kill "$CPA_PID" 2>/dev/null || true
  exit 1
fi
"$ROOT_DIR/cpa-manager" --no-start-cpa > "$ROOT_DIR/logs/cpa-manager.log" 2>&1 &
MANAGER_PID=$!
if ! "$ROOT_DIR/cpa-updater" --record-runtime --runtime-cpa-pid "$CPA_PID" --runtime-manager-pid "$MANAGER_PID" --runtime-config "$CONFIG_PATH"; then
  kill "$MANAGER_PID" 2>/dev/null || true
  wait "$MANAGER_PID" 2>/dev/null || true
  "$ROOT_DIR/cpa-updater" --stop || true
  exit 1
fi
echo "CLIProxyAPI started (PID $CPA_PID); CPA-Manager started (PID $MANAGER_PID)."
SH
  chmod +x "$PKG_DIR/start.sh"
  cat > "$PKG_DIR/stop.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$ROOT_DIR/cpa-updater" --stop
SH
  chmod +x "$PKG_DIR/stop.sh"
fi

cat > "$PKG_DIR/SUITE-README.md" <<'MD'
# CLIProxyAPI Suite

- Start with `start.sh` (Linux/macOS) or `start.bat` (Windows). First launch creates `config.yaml` from `config.example.yaml`.
- Select a different CPA configuration with `--config <path>`.
- Stop only this launcher's recorded processes with `stop.sh` or `stop.bat`.
- The Management Center's System page checks for updates and downloads packages; it does not install them.
- With both services running from the launcher, run `cpa-updater` (`cpa-updater.exe` on Windows) in this directory for automatic update and restart. Use `--check` to check versions without installation.
- The updater verifies the release checksum, preserves configuration, auth files, databases, logs, and plugin settings, and rolls back on a failed health check.
MD
cat > "$PKG_DIR/SUITE-README_CN.md" <<'MD'
# CLIProxyAPI 套件

- Linux/macOS 使用 `start.sh` 启动，Windows 使用 `start.bat`。首次启动会从 `config.example.yaml` 创建 `config.yaml`。
- 使用 `--config <path>` 指定其他 CPA 配置文件。
- 使用对应的 `stop.sh` 或 `stop.bat` 停止启动脚本记录的进程。
- 管理面板“系统”页仅检查更新并下载安装包，不会自动安装。
- 通过启动脚本启动的两个服务都在运行时，在程序目录运行 `cpa-updater`（Windows 为 `cpa-updater.exe`）可自动下载、校验、安装并重启；`--check` 只检查版本。
- 更新器会校验发布包 SHA-256，保留配置、认证文件、数据库、日志和插件设置；健康检查失败时会回滚。
MD

python3 - "$VERSION_NO_V" "$MANAGER_VERSION" "$PKG_DIR/suite-version.json" <<'PY'
import json
import sys
from pathlib import Path

cpa_version, manager_version, path = sys.argv[1:]
Path(path).write_text(json.dumps({
    "schema": 1,
    "releaseTag": "v" + cpa_version,
    "cpaVersion": cpa_version,
    "managerVersion": manager_version,
}, indent=2) + "\n", encoding="utf-8")
PY

cp LICENSE "$PKG_DIR/"
cp README.md "$PKG_DIR/"
cp README_CN.md "$PKG_DIR/"
cp config.example.yaml "$PKG_DIR/"
if [[ -n "$MANAGEMENT_HTML" ]]; then
  cp "$MANAGEMENT_HTML" "$PKG_DIR/static/management.html"
fi

mkdir -p "$OUTPUT_DIR"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}"
if [[ ! "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a non-negative integer" >&2
  exit 1
fi
python3 - "$PKG_DIR" "$OUTPUT_DIR" "$PKG_NAME" "$TARGET_OS" "$SOURCE_DATE_EPOCH" <<'PY'
import gzip
import io
import os
import stat
import sys
import tarfile
import time
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

source = Path(sys.argv[1])
output_dir = Path(sys.argv[2])
package_name = sys.argv[3]
target_os = sys.argv[4]
epoch = int(sys.argv[5])


def mode_for(path: Path) -> int:
    if path.is_dir():
        return 0o755
    if path.is_symlink():
        return 0o777
    return 0o755 if os.access(path, os.X_OK) else 0o644


def entries(root: Path):
    yield root
    yield from sorted(root.rglob('*'), key=lambda item: item.relative_to(root.parent).as_posix())

if target_os == 'windows':
    destination = output_dir / f'{package_name}.zip'
    dos_epoch = max(epoch, 315532800)
    date_time = time.gmtime(dos_epoch)[:6]
    with ZipFile(destination, 'w', compression=ZIP_DEFLATED, compresslevel=9) as archive:
        for path in entries(source):
            name = path.relative_to(source.parent).as_posix()
            if path.is_dir():
                name += '/'
                info = ZipInfo(name, date_time)
                info.external_attr = (0o755 & 0xFFFF) << 16 | 0x10
                info.create_system = 3
                archive.writestr(info, b'')
            elif path.is_symlink():
                info = ZipInfo(name, date_time)
                info.external_attr = (0o777 & 0xFFFF) << 16
                info.create_system = 3
                archive.writestr(info, os.readlink(path).encode())
            else:
                info = ZipInfo(name, date_time)
                info.compress_type = ZIP_DEFLATED
                info.external_attr = (mode_for(path) & 0xFFFF) << 16
                info.create_system = 3
                archive.writestr(info, path.read_bytes())
    print(destination)
else:
    destination = output_dir / f'{package_name}.tar.gz'
    tar_buffer = io.BytesIO()
    with tarfile.open(fileobj=tar_buffer, mode='w', format=tarfile.USTAR_FORMAT) as archive:
        for path in entries(source):
            name = path.relative_to(source.parent).as_posix()
            info = tarfile.TarInfo(name)
            info.mtime = epoch
            info.uid = 0
            info.gid = 0
            info.uname = ''
            info.gname = ''
            info.mode = mode_for(path)
            if path.is_symlink():
                info.type = tarfile.SYMTYPE
                info.linkname = os.readlink(path)
                archive.addfile(info)
            elif path.is_dir():
                info.type = tarfile.DIRTYPE
                archive.addfile(info)
            else:
                data = path.read_bytes()
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
    with destination.open('wb') as output:
        with gzip.GzipFile(fileobj=output, mode='wb', compresslevel=9, mtime=0) as compressed:
            compressed.write(tar_buffer.getvalue())
    print(destination)
PY
