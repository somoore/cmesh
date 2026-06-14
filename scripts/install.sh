#!/bin/sh
set -eu

die() {
  printf '%s\n' "cmesh install: $*" >&2
  exit 1
}

info() {
  printf '%s\n' "cmesh install: $*"
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

detect_os() {
  os=$(uname -s 2>/dev/null || printf unknown)
  case "$os" in
    Darwin) printf darwin ;;
    Linux) printf linux ;;
    FreeBSD) printf freebsd ;;
    MINGW*|MSYS*|CYGWIN*) printf windows ;;
    *) die "unsupported operating system: $os" ;;
  esac
}

detect_arch() {
  arch=$(uname -m 2>/dev/null || printf unknown)
  case "$arch" in
    x86_64|amd64) printf amd64 ;;
    arm64|aarch64) printf arm64 ;;
    *) die "unsupported architecture: $arch" ;;
  esac
}

install_file() {
  src=$1
  dst=$2
  mkdir -p "$(dirname "$dst")"
  tmp="$dst.tmp.$$"
  cp "$src" "$tmp"
  chmod 0755 "$tmp"
  mv "$tmp" "$dst"
}

install_from_binary() {
  [ -n "${CMESH_BIN:-}" ] || return 1
  [ -f "$CMESH_BIN" ] || die "CMESH_BIN does not name a file: $CMESH_BIN"
  install_file "$CMESH_BIN" "$install_path"
  return 0
}

install_from_source() {
  [ -d "./cmd/cmesh" ] || return 1
  need_cmd go
  tmpdir="${TMPDIR:-/tmp}/cmesh-install.$$"
  mkdir -p "$tmpdir"
  trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
  info "building ./cmd/cmesh"
  go build -trimpath -o "$tmpdir/cmesh" ./cmd/cmesh
  install_file "$tmpdir/cmesh" "$install_path"
  return 0
}

download() {
  url=$1
  out=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
  else
    die "curl or wget is required for release downloads"
  fi
}

latest_version() {
  repo=$1
  need_cmd sed
  url="https://api.github.com/repos/$repo/releases/latest"
  tmp="${TMPDIR:-/tmp}/cmesh-latest.$$"
  download "$url" "$tmp"
  version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp" | head -n 1)
  rm -f "$tmp"
  [ -n "$version" ] || die "could not determine latest release for $repo"
  printf '%s' "$version"
}

# install_from_release downloads a prebuilt cmesh binary from a GitHub release.
#
# Release URL pattern (PLACEHOLDER until the public repo/CDN is published):
#   https://github.com/<owner>/<repo>/releases/download/<version>/cmesh_<os>_<arch>.tar.gz
# Override the source with:
#   CMESH_REPO=owner/repo      # e.g. cmesh/cmesh (default below is a placeholder)
#   CMESH_VERSION=v1.2.3        # or "latest" (default)
# When get.cmesh.dev is live this script ships with CMESH_REPO baked in.
install_from_release() {
  : "${CMESH_REPO:=cmesh/cmesh}" # PLACEHOLDER public repo; override via env.
  need_cmd tar
  os=$(detect_os)
  arch=$(detect_arch)
  version=${CMESH_VERSION:-}
  if [ -z "$version" ] || [ "$version" = "latest" ]; then
    version=$(latest_version "$CMESH_REPO")
  fi
  asset="cmesh_${os}_${arch}.tar.gz"
  url="https://github.com/$CMESH_REPO/releases/download/$version/$asset"
  tmpdir="${TMPDIR:-/tmp}/cmesh-install.$$"
  mkdir -p "$tmpdir"
  trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
  info "downloading $url"
  download "$url" "$tmpdir/$asset"
  tar -xzf "$tmpdir/$asset" -C "$tmpdir"
  [ -f "$tmpdir/cmesh" ] || die "release archive did not contain cmesh binary"
  install_file "$tmpdir/cmesh" "$install_path"
  return 0
}

install_dir=${CMESH_INSTALL_DIR:-}
if [ -z "$install_dir" ]; then
  if [ "$(id -u 2>/dev/null || printf 1)" = "0" ]; then
    install_dir=/usr/local/bin
  else
    install_dir="$HOME/.local/bin"
  fi
fi

install_path="$install_dir/cmesh"

if install_from_binary; then
  info "installed $install_path from CMESH_BIN"
elif install_from_source; then
  info "installed $install_path from source"
elif install_from_release; then
  info "installed $install_path from GitHub release"
else
  die "no install source found; set CMESH_BIN, run from a source checkout containing ./cmd/cmesh, or set CMESH_REPO=owner/repo"
fi

# Print the installed version so the user can confirm the binary works.
if [ -x "$install_path" ]; then
  printf 'cmesh install: version '
  "$install_path" version 2>/dev/null || printf '(unknown)\n'
fi

on_path=0
case ":$PATH:" in
  *":$install_dir:"*) on_path=1 ;;
  *) info "add $install_dir to PATH to run cmesh from any shell:"
     info "  export PATH=\"$install_dir:\$PATH\"" ;;
esac

# Suggest the next step: the native interactive agent picker (Part 2).
info ""
if [ "$on_path" = "1" ]; then
  info "Next, wire cmesh into your AI coding agent:"
  info "  cmesh install"
else
  info "Next, wire cmesh into your AI coding agent (after fixing PATH):"
  info "  $install_path install"
fi
