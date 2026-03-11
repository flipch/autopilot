#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION_FILE="${ROOT_DIR}/VERSION"
CHANGELOG_FILE="${ROOT_DIR}/CHANGELOG.md"

if [ $# -lt 1 ]; then
  echo "usage: scripts/release.sh <version>" >&2
  exit 1
fi

VERSION="$1"
TAG="v${VERSION#v}"

cd "$ROOT_DIR"

printf '%s\n' "${TAG#v}" > "$VERSION_FILE"

if ! grep -q "## \[${TAG#v}\]" "$CHANGELOG_FILE"; then
  echo "error: changelog is missing an entry for ${TAG#v}" >&2
  exit 1
fi

go test ./...

git add VERSION CHANGELOG.md README.md scripts/install.sh scripts/release.sh goreleaser.yaml .github/workflows/release.yml cmd/autopilot/main.go internal/autopilot/autopilot.go internal/autopilot/autopilot_test.go
git commit -m "chore: release ${TAG}" || true
git tag -a "$TAG" -m "Release ${TAG}"
git push origin HEAD
git push origin "$TAG"

NOTES_FILE=$(mktemp)
trap 'rm -f "$NOTES_FILE"' EXIT
python3 - "$CHANGELOG_FILE" "${TAG#v}" > "$NOTES_FILE" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
version = sys.argv[2]
content = path.read_text()
start = f"## [{version}]"
idx = content.find(start)
if idx == -1:
    raise SystemExit(f"missing changelog section for {version}")
rest = content[idx:]
next_idx = rest.find("\n## [", len(start))
section = rest if next_idx == -1 else rest[:next_idx]
print(section.strip())
PY

gh release create "$TAG" --title "$TAG" --notes-file "$NOTES_FILE"
