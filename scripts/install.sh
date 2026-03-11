#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="flipch"
REPO_NAME="autopilot"
DEST_PATH="/usr/local/bin/autopilot"
BINARY_NAME="autopilot"
GH_API_ACCEPT="Accept: application/vnd.github+json"
RELEASE_LATEST_URL="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest"
RELEASE_TAG_URL="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/tags"

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

detect_downloader() {
  if command_exists curl; then
    DOWNLOADER="curl"
  elif command_exists wget; then
    DOWNLOADER="wget"
  else
    echo "error: curl or wget is required to download releases" >&2
    exit 1
  fi
}

fetch_release_json() {
  local url="$1"
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL -H "$GH_API_ACCEPT" "$url"
  else
    wget -qO- --header="$GH_API_ACCEPT" "$url"
  fi
}

resolve_python() {
  if command_exists python3; then
    PYTHON_CMD=python3
  elif command_exists python; then
    PYTHON_CMD=python
  else
    echo "error: python3 or python is required to parse release metadata" >&2
    exit 1
  fi
}

format_version_tag() {
  local version="$1"
  if [[ "$version" == v* ]]; then
    printf '%s' "$version"
  else
    printf 'v%s' "$version"
  fi
}

parse_version_from_json() {
  local release_json="$1"
  RELEASE_JSON="$release_json" "$PYTHON_CMD" - <<'PY'
import json
import os
import sys

try:
    print(json.loads(os.environ["RELEASE_JSON"])["tag_name"])
except Exception as exc:
    print(f"error: unable to read tag_name: {exc}", file=sys.stderr)
    sys.exit(1)
PY
}

detect_platform() {
  local raw_os raw_arch
  raw_os=$(uname -s)
  raw_arch=$(uname -m)

  case "$raw_os" in
    Linux|linux*) PLATFORM_OS=linux ;;
    Darwin|darwin*) PLATFORM_OS=darwin ;;
    *[Mm]INGW*|*MSYS*|*CYGWIN*) PLATFORM_OS=windows ;;
    *)
      echo "error: unsupported OS '$raw_os'" >&2
      exit 1
      ;;
  esac

  case "$raw_arch" in
    x86_64|amd64) PLATFORM_ARCH=amd64 ;;
    arm64|aarch64) PLATFORM_ARCH=arm64 ;;
    *)
      echo "error: unsupported architecture '$raw_arch'" >&2
      exit 1
      ;;
  esac
}

query_asset_url() {
  local release_json="$1"
  local asset_name="$2"
  RELEASE_JSON="$release_json" TARGET_ASSET="$asset_name" "$PYTHON_CMD" - <<'PY'
import itertools
import json
import os
import sys

data = json.loads(os.environ["RELEASE_JSON"])
target = os.environ["TARGET_ASSET"]
assets = data.get("assets", [])
for asset in assets:
    if asset.get("name") == target:
        print(asset.get("browser_download_url"))
        sys.exit(0)
print(f"error: release does not contain asset '{target}'", file=sys.stderr)
if assets:
    print("available assets:", file=sys.stderr)
    for name in itertools.islice((asset.get("name") for asset in assets if asset.get("name")), None):
        print(f"  - {name}", file=sys.stderr)
sys.exit(1)
PY
}

main() {
  detect_downloader
  resolve_python
  detect_platform

  local release_json=""
  if [ -z "${AUTOPILOT_VERSION:-}" ]; then
    release_json=$(fetch_release_json "$RELEASE_LATEST_URL")
  else
    local candidate
    for candidate in "$AUTOPILOT_VERSION" "$(format_version_tag "$AUTOPILOT_VERSION")"; do
      if [ -z "$candidate" ]; then
        continue
      fi
      if release_json=$(fetch_release_json "$RELEASE_TAG_URL/$candidate"); then
        break
      fi
      release_json=""
    done
    if [ -z "$release_json" ]; then
      echo "error: release '${AUTOPILOT_VERSION}' not found" >&2
      exit 1
    fi
  fi

  local resolved_tag
  resolved_tag=$(parse_version_from_json "$release_json")

  local asset_base="$BINARY_NAME"
  if [ "$PLATFORM_OS" = "windows" ]; then
    asset_base="${asset_base}.exe"
  fi

  local asset_name="${asset_base}_${resolved_tag}_${PLATFORM_OS}_${PLATFORM_ARCH}"
  local asset_url
  asset_url=$(query_asset_url "$release_json" "$asset_name")

  local tmpfile
  tmpfile=$(mktemp)
  trap 'rm -f "$tmpfile"' EXIT

  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL "$asset_url" -o "$tmpfile"
  else
    wget -qO "$tmpfile" "$asset_url"
  fi

  if mv "$tmpfile" "$DEST_PATH" 2>/dev/null; then
    :
  else
    if command_exists sudo; then
      echo "elevated move: /usr/local/bin requires sudo" >&2
      sudo mv "$tmpfile" "$DEST_PATH"
    else
      echo "error: could not move the binary to '$DEST_PATH' (permission denied)" >&2
      exit 1
    fi
  fi

  chmod +x "$DEST_PATH"
  echo "autopilot ${resolved_tag} installed to ${DEST_PATH}"
}

main "$@"
