#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="flipch"
REPO_NAME="autopilot"
DEST_PATH="/usr/local/bin/autopilot"
BINARY_NAME="autopilot"

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

require_command() {
  if ! command_exists "$1"; then
    echo "error: required command '$1' is not available" >&2
    exit 1
  fi
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

format_version_tag() {
  local version="$1"
  if [[ "$version" == v* ]]; then
    printf '%s' "$version"
  else
    printf 'v%s' "$version"
  fi
}

main() {
  require_command gh
  detect_platform

  local resolved_tag
  if [ -z "${AUTOPILOT_VERSION:-}" ]; then
    resolved_tag=$(gh release view --repo "${REPO_OWNER}/${REPO_NAME}" --json tagName --jq '.tagName')
  else
    resolved_tag=$(format_version_tag "$AUTOPILOT_VERSION")
  fi

  local asset_base="$BINARY_NAME"
  if [ "$PLATFORM_OS" = "windows" ]; then
    asset_base="${asset_base}.exe"
  fi

  local asset_name="${asset_base}_${resolved_tag}_${PLATFORM_OS}_${PLATFORM_ARCH}"

  local tmpdir
  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT

  gh release download "$resolved_tag" --repo "${REPO_OWNER}/${REPO_NAME}" --pattern "$asset_name" --dir "$tmpdir"

  local tmpfile="$tmpdir/$asset_name"

  if [ ! -f "$tmpfile" ]; then
    echo "error: release asset '$asset_name' was not downloaded" >&2
    exit 1
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
