#!/usr/bin/env bash
set -euo pipefail

VERSION=""
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
if [[ -z "$VERSION" || -z "$TARGET_OS" || -z "$TARGET_ARCH" || -z "$SERVER_BIN" || -z "$OUTPUT_DIR" ]]; then
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
  if [[ -n "$USAGE_BIN" ]]; then
    cp "$USAGE_BIN" "$PKG_DIR/cpa-manager.exe"
  fi
  if [[ -n "$UPDATER_BIN" ]]; then
    cp "$UPDATER_BIN" "$PKG_DIR/cpa-updater.exe"
  fi
  cat > "$PKG_DIR/start.bat" <<'BAT'
@echo off
setlocal
cd /d %~dp0
cpa-manager.exe
BAT
else
  cp "$SERVER_BIN" "$PKG_DIR/cli-proxy-api"
  if [[ -n "$USAGE_BIN" ]]; then
    cp "$USAGE_BIN" "$PKG_DIR/cpa-manager"
    chmod +x "$PKG_DIR/cpa-manager"
  fi
  if [[ -n "$UPDATER_BIN" ]]; then
    cp "$UPDATER_BIN" "$PKG_DIR/cpa-updater"
    chmod +x "$PKG_DIR/cpa-updater"
  fi
  chmod +x "$PKG_DIR/cli-proxy-api"
  cat > "$PKG_DIR/start.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"
exec ./cpa-manager
SH
  chmod +x "$PKG_DIR/start.sh"
fi

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
