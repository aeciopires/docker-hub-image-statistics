#!/usr/bin/env bash
# Shared helpers for the scripts in this directory (bash 3.2 compatible: macOS).

if [ -t 1 ]; then
  C_RED=$'\033[31m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_BOLD=$'\033[1m'; C_RESET=$'\033[0m'
else
  C_RED=""; C_GREEN=""; C_YELLOW=""; C_BOLD=""; C_RESET=""
fi

info()  { printf '%s==>%s %s\n' "$C_BOLD" "$C_RESET" "$*"; }
ok()    { printf '  %s✔%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
fail()  { printf '  %s✘%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
die()   { fail "$*"; exit 1; }

# detect_platform sets OS_ID (ubuntu|macos|other), OS_VERSION and ARCH (amd64|arm64).
detect_platform() {
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *) ARCH="$(uname -m)" ;;
  esac
  case "$(uname -s)" in
    Darwin)
      OS_ID=macos
      OS_VERSION="$(sw_vers -productVersion 2>/dev/null || echo unknown)"
      ;;
    Linux)
      OS_ID=other; OS_VERSION=unknown
      if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091
        . /etc/os-release
        [ "${ID:-}" = "ubuntu" ] && OS_ID=ubuntu
        OS_VERSION="${VERSION_ID:-unknown}"
      fi
      ;;
    *) OS_ID=other; OS_VERSION=unknown ;;
  esac
}

# supported_platform returns 0 for the officially supported combinations.
supported_platform() {
  case "$OS_ID/$OS_VERSION/$ARCH" in
    ubuntu/22.04/amd64|ubuntu/24.04/amd64|ubuntu/26.04/amd64) return 0 ;;
    macos/*/amd64|macos/*/arm64) return 0 ;;
  esac
  return 1
}

# sed_inplace works with both GNU sed (Linux) and BSD sed (macOS).
sed_inplace() {
  local expr="$1" file="$2"
  sed -e "$expr" "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}
