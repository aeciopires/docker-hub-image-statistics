#!/usr/bin/env bash
# Extracts every ```mermaid block from the Markdown files and renders it with
# the official mermaid-cli Docker image. Fails if any diagram is invalid.
# Usage: scripts/validate-mermaid.sh [files...]   (or: make docs-validate)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib.sh
. "$ROOT/scripts/lib.sh"
IMAGE="${MERMAID_IMAGE:-minlag/mermaid-cli:12.0.1}"

if [ "$#" -gt 0 ]; then
  files="$*"
else
  files="$(cd "$ROOT" && find . -name '*.md' -not -path './.git/*' -not -path './dist/*' | sort)"
fi

# Keep the work directory inside the repository: Colima (macOS) only shares
# $HOME with its VM, and macOS mktemp uses /var/folders, invisible to Docker.
mkdir -p "$ROOT/.tmp"
WORK="$(mktemp -d "$ROOT/.tmp/mermaid.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
chmod 0777 "$WORK"

count=0
for f in $files; do
  awk -v out="$WORK" -v src="$(echo "$f" | tr '/.' '__')" '
    /^```mermaid[[:space:]]*$/ { inside=1; n++; file=sprintf("%s/%s__%02d.mmd", out, src, n); next }
    /^```[[:space:]]*$/ && inside { inside=0; close(file); next }
    inside { print > file }
  ' "$ROOT/$f"
done
count="$(find "$WORK" -name '*.mmd' | wc -l | tr -d ' ')"
[ "$count" -gt 0 ] || { warn "no mermaid diagrams found"; exit 0; }
info "Validating $count mermaid diagram(s) with $IMAGE"

failed=0
for mmd in "$WORK"/*.mmd; do
  name="$(basename "$mmd")"
  if docker run --rm -u "$(id -u):$(id -g)" -v "$WORK:/data" "$IMAGE" \
      -q -i "/data/$name" -o "/data/${name%.mmd}.svg" >"$WORK/${name}.log" 2>&1; then
    ok "$name"
  else
    fail "$name"; sed 's/^/      /' "$WORK/${name}.log" | head -20 >&2; failed=$((failed + 1))
  fi
done
[ "$failed" -eq 0 ] || die "$failed invalid diagram(s)"
ok "all diagrams are valid"
