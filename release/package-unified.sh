#!/usr/bin/env bash
set -euo pipefail

VERSION=""
TARGET_OS=""
TARGET_ARCH=""
SERVER_BIN=""
USAGE_BIN=""
MANAGEMENT_HTML=""
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
    --management-html)
      MANAGEMENT_HTML="$2"
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

if [[ -z "$VERSION" || -z "$TARGET_OS" || -z "$TARGET_ARCH" || -z "$SERVER_BIN" || -z "$MANAGEMENT_HTML" || -z "$OUTPUT_DIR" ]]; then
  echo "Missing required arguments" >&2
  exit 1
fi

if [[ ! -f "$SERVER_BIN" ]]; then
  echo "Server binary not found: $SERVER_BIN" >&2
  exit 1
fi

if [[ -n "$USAGE_BIN" && ! -f "$USAGE_BIN" ]]; then
  echo "Usage-service binary not found: $USAGE_BIN" >&2
  exit 1
fi

if [[ ! -f "$MANAGEMENT_HTML" ]]; then
  echo "Management HTML not found: $MANAGEMENT_HTML" >&2
  exit 1
fi

VERSION_NO_V="${VERSION#v}"
PKG_NAME="CLIProxyAPI_${VERSION_NO_V}_${TARGET_OS}_${TARGET_ARCH}"
PKG_DIR="${OUTPUT_DIR}/${PKG_NAME}"

rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR/static"

if [[ "$TARGET_OS" == "windows" ]]; then
  cp "$SERVER_BIN" "$PKG_DIR/cli-proxy-api.exe"
  if [[ -n "$USAGE_BIN" ]]; then
    cp "$USAGE_BIN" "$PKG_DIR/usage-service.exe"
  fi
  cat > "$PKG_DIR/start.bat" <<'BAT'
@echo off
setlocal
cd /d %~dp0
if exist usage-service.exe start "usage-service" usage-service.exe
cli-proxy-api.exe --config config.example.yaml
BAT
else
  cp "$SERVER_BIN" "$PKG_DIR/cli-proxy-api"
  if [[ -n "$USAGE_BIN" ]]; then
    cp "$USAGE_BIN" "$PKG_DIR/usage-service"
    chmod +x "$PKG_DIR/usage-service"
  fi
  chmod +x "$PKG_DIR/cli-proxy-api"
  cat > "$PKG_DIR/start.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"
if [[ -x "./usage-service" ]]; then
  ./usage-service &
fi
exec ./cli-proxy-api --config config.example.yaml
SH
  chmod +x "$PKG_DIR/start.sh"
fi

cp LICENSE "$PKG_DIR/"
cp README.md "$PKG_DIR/"
cp README_CN.md "$PKG_DIR/"
cp config.example.yaml "$PKG_DIR/"
cp "$MANAGEMENT_HTML" "$PKG_DIR/static/management.html"

mkdir -p "$OUTPUT_DIR"
if [[ "$TARGET_OS" == "windows" ]]; then
  (cd "$OUTPUT_DIR" && zip -r "${PKG_NAME}.zip" "$PKG_NAME" >/dev/null)
  echo "${OUTPUT_DIR}/${PKG_NAME}.zip"
else
  tar -C "$OUTPUT_DIR" -czf "${OUTPUT_DIR}/${PKG_NAME}.tar.gz" "$PKG_NAME"
  echo "${OUTPUT_DIR}/${PKG_NAME}.tar.gz"
fi
