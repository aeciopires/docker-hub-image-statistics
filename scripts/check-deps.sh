#!/usr/bin/env bash
# Checks that every development dependency is installed.
# Usage: scripts/check-deps.sh   (or: make check-deps)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib.sh
. "$ROOT/scripts/lib.sh"

missing=0
detect_platform
info "Platform: $OS_ID $OS_VERSION ($ARCH)"
if supported_platform; then ok "supported platform"; else warn "untested platform (supported: Ubuntu 22.04/24.04/26.04 amd64, macOS amd64/arm64)"; fi

check_cmd() { # name, version command...
  local name="$1"; shift
  # Capture everything first: "| head" would SIGPIPE the command under pipefail.
  if out="$("$@" 2>/dev/null)" && [ -n "$out" ]; then
    ok "$name: ${out%%$'\n'*}"
  else
    fail "$name: not found"; missing=$((missing + 1))
  fi
}

info "System tools"
check_cmd git git --version
check_cmd make make --version
check_cmd curl curl --version
check_cmd python3 python3 --version

info "Container runtime (installed separately)"
if [ "$OS_ID" = "macos" ]; then
  # macOS runs Docker inside a Colima VM (docs/requirements/macos.md).
  check_cmd colima colima version
  if command -v colima >/dev/null 2>&1; then
    if colima status >/dev/null 2>&1; then
      ok "Colima is running"
    else
      fail "Colima is not running (run: make colima-start)"; missing=$((missing + 1))
    fi
  fi
  context="$(docker context show 2>/dev/null || true)"
  case "$context" in
    colima*) ok "docker context: $context" ;;
    *) warn "docker context is '${context:-unknown}', expected colima (run: docker context use colima)" ;;
  esac
fi
check_cmd docker docker --version
check_cmd "docker compose" docker compose version
check_cmd "docker buildx" docker buildx version
if command -v docker >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
  if [ "$OS_ID" = "macos" ]; then
    warn "docker daemon not reachable (run: make colima-start)"
  else
    warn "docker daemon not reachable (is it running? is your user in the docker group?)"
  fi
fi

info "Toolchain managed by mise (.mise.toml)"
if command -v mise >/dev/null 2>&1; then
  ok "mise: $(mise --version | head -n1)"
  cd "$ROOT"
  check_cmd go mise exec -- go version
  check_cmd golangci-lint mise exec -- golangci-lint version
  check_cmd govulncheck mise exec -- govulncheck -version
  check_cmd trivy mise exec -- trivy --version
  check_cmd helm mise exec -- helm version --short
  check_cmd helm-docs mise exec -- helm-docs --version
  check_cmd kubeconform mise exec -- kubeconform -v
  check_cmd hadolint mise exec -- hadolint --version
  check_cmd kind mise exec -- kind version
  check_cmd kubectl mise exec -- kubectl version --client
  helm_version="$(mise exec -- helm version --short 2>/dev/null || true)"
  case "$helm_version" in
    v4.*) ok "Helm 4 detected" ;;
    *) fail "Helm 4 is required (found: ${helm_version:-none})"; missing=$((missing + 1)) ;;
  esac
else
  fail "mise: not found (run: make install-deps)"; missing=$((missing + 1))
fi

echo
if [ "$missing" -gt 0 ]; then
  die "$missing dependency(ies) missing. Run: make install-deps"
fi
ok "all dependencies are installed"
