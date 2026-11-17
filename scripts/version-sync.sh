#!/usr/bin/env bash
# Propagates the version in the VERSION file to the Helm chart and docs.
# Usage: scripts/version-sync.sh          # update files
#        scripts/version-sync.sh --check  # fail if any file is out of sync (CI)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib.sh
. "$ROOT/scripts/lib.sh"

VERSION="$(tr -d ' \n\r' < "$ROOT/VERSION")"
SEMVER='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
echo "$VERSION" | grep -Eq "$SEMVER" || die "VERSION '$VERSION' is not a semantic version (https://semver.org)"

CHART="$ROOT/charts/docker-hub-image-statistics/Chart.yaml"
CHANGELOG="$ROOT/CHANGELOG.md"

if [ "${1:-}" = "--check" ]; then
  errors=0
  grep -q "^version: $VERSION\$" "$CHART" || { fail "Chart.yaml version != $VERSION"; errors=1; }
  grep -q "^appVersion: \"$VERSION\"\$" "$CHART" || { fail "Chart.yaml appVersion != $VERSION"; errors=1; }
  grep -q "^## \[$VERSION\]" "$CHANGELOG" || { fail "CHANGELOG.md has no entry for [$VERSION]"; errors=1; }
  [ "$errors" -eq 0 ] || die "run: make version-sync"
  ok "everything is at version $VERSION"
  exit 0
fi

sed_inplace "s/^version: .*/version: $VERSION/" "$CHART"
sed_inplace "s/^appVersion: .*/appVersion: \"$VERSION\"/" "$CHART"
ok "Chart.yaml -> $VERSION"
grep -q "^## \[$VERSION\]" "$CHANGELOG" || warn "add a '## [$VERSION] - YYYY-MM-DD' section to CHANGELOG.md"
if command -v mise >/dev/null 2>&1; then
  (cd "$ROOT/charts/docker-hub-image-statistics" && mise exec -- helm-docs --template-files README.md.gotmpl >/dev/null) && ok "chart README regenerated"
fi
