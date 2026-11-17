#!/usr/bin/env bash
# Installs the development dependencies (except Docker, installed separately).
# Supported: Ubuntu 22.04/24.04/26.04 (amd64) and macOS (amd64/arm64).
# Usage: scripts/install-deps.sh   (or: make install-deps)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib.sh
. "$ROOT/scripts/lib.sh"

detect_platform
info "Platform: $OS_ID $OS_VERSION ($ARCH)"
supported_platform || warn "untested platform; continuing anyway"

case "$OS_ID" in
  ubuntu)
    info "Installing system packages with apt (sudo may ask for your password)"
    SUDO=""; [ "$(id -u)" -ne 0 ] && SUDO="sudo"
    $SUDO apt-get update -y
    $SUDO apt-get install -y --no-install-recommends ca-certificates curl git make python3 tar gzip
    ;;
  macos)
    info "Checking Xcode Command Line Tools (git, make)"
    if ! xcode-select -p >/dev/null 2>&1; then
      warn "installing Xcode Command Line Tools; re-run this script when it finishes"
      xcode-select --install || true
      exit 1
    fi
    ok "Xcode Command Line Tools present"
    ;;
  *)
    warn "unknown OS: make sure curl, git, make and python3 are installed"
    ;;
esac

if ! command -v mise >/dev/null 2>&1; then
  info "Installing mise"
  if [ "$OS_ID" = "macos" ] && command -v brew >/dev/null 2>&1; then
    brew install mise
  else
    # Official installer: https://mise.jdx.dev/installing-mise.html
    curl -fsSL https://mise.run | sh
    export PATH="$HOME/.local/bin:$PATH"
  fi
  warn "activate mise in your shell: https://mise.jdx.dev/getting-started.html#activate-mise"
fi
ok "mise: $(mise --version | head -n1)"

info "Installing the toolchain declared in .mise.toml"
cd "$ROOT"
mise trust --yes "$ROOT/.mise.toml" >/dev/null 2>&1 || mise trust "$ROOT/.mise.toml"
mise install

if [ "$OS_ID" = "macos" ]; then
  if ! command -v colima >/dev/null 2>&1 || ! command -v docker >/dev/null 2>&1; then
    warn "Docker on macOS runs with Colima (installed separately):"
    warn "  brew install colima docker docker-compose docker-buildx && make colima-start"
    warn "  see docs/requirements/macos.md"
  fi
elif ! command -v docker >/dev/null 2>&1; then
  warn "Docker Engine is not installed: see docs/requirements/ubuntu.md"
fi

info "Verifying"
"$ROOT/scripts/check-deps.sh"
