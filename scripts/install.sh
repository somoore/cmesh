#!/bin/sh
set -eu

# --no-agent-setup: install the binary only; do not auto-launch the agent picker.
no_agent_setup=0
CMESH_REPO=${CMESH_REPO:-somoore/cmesh}
for _arg in "$@"; do
  case "$_arg" in
    --no-agent-setup) no_agent_setup=1 ;;
  esac
done

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

can_use_dev_tty() {
  [ -r /dev/tty ] && [ -w /dev/tty ] && ( : </dev/tty >/dev/tty ) 2>/dev/null
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

# source_checkout_dir returns a directory that contains ./cmd/cmesh, checking the
# current working directory first and then the script's own parent (so running
# `sh scripts/install.sh` from inside scripts/ still finds the repo root). Prints
# nothing and returns non-zero when no source checkout is found.
source_checkout_dir() {
  if [ -d "./cmd/cmesh" ]; then
    printf '%s' "."
    return 0
  fi
  # $0 may be relative; resolve its directory, then check its parent (repo root).
  # Unset CDPATH so `cd` never prints a path or jumps to a CDPATH entry.
  script_dir=$(unset CDPATH; cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || return 1
  if [ -d "$script_dir/../cmd/cmesh" ]; then
    printf '%s' "$script_dir/.."
    return 0
  fi
  return 1
}

install_from_source() {
  src=$(source_checkout_dir) || return 1
  need_cmd go
  tmpdir="${TMPDIR:-/tmp}/cmesh-install.$$"
  mkdir -p "$tmpdir"
  trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
  info "building cmesh from source in $src"
  ( cd "$src" && go build -trimpath -o "$tmpdir/cmesh" ./cmd/cmesh )
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

# latest_version prints the newest release tag for $repo, or returns non-zero on
# failure. It must NOT call die: it runs in a $(...) subshell, so die would only
# exit the subshell and leave an EMPTY version that builds a broken
# .../download//cmesh_... URL. The caller checks the exit status instead.
latest_version() {
  repo=$1
  need_cmd awk
  tmp="${TMPDIR:-/tmp}/cmesh-latest.$$"

  # GitHub's /releases/latest intentionally excludes pre-releases. Try it first
  # for stable releases, then fall back to the newest published release so
  # pre-alpha users can still install with CMESH_VERSION=latest.
  url="https://api.github.com/repos/$repo/releases/latest"
  if ! download "$url" "$tmp" 2>/dev/null; then
    url="https://api.github.com/repos/$repo/releases"
    if ! download "$url" "$tmp" 2>/dev/null; then
      rm -f "$tmp"
      return 1
    fi
  fi

  version=$(awk '
    match($0, /"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"/) {
      value = substr($0, RSTART, RLENGTH)
      sub(/.*"tag_name"[[:space:]]*:[[:space:]]*"/, "", value)
      sub(/".*/, "", value)
      print value
      exit
    }
  ' "$tmp")
  rm -f "$tmp"
  [ -n "$version" ] || return 1
  printf '%s' "$version"
}

# install_from_release downloads a prebuilt cmesh binary from a GitHub release.
#
# Release URL pattern:
#   https://github.com/<owner>/<repo>/releases/download/<version>/cmesh_<os>_<arch>.tar.gz
# Override the source with:
#   CMESH_REPO=owner/repo      # default: somoore/cmesh
#   CMESH_VERSION=v1.2.3        # or "latest" (default)
install_from_release() {
  need_cmd tar
  os=$(detect_os)
  arch=$(detect_arch)
  version=${CMESH_VERSION:-}
  if [ -z "$version" ] || [ "$version" = "latest" ]; then
    if ! version=$(latest_version "$CMESH_REPO"); then
      die "could not determine the latest release for $CMESH_REPO (set CMESH_VERSION=vX.Y.Z, CMESH_BIN=/path/to/cmesh, or run from a source checkout)"
    fi
  fi
  asset="cmesh_${os}_${arch}.tar.gz"
  url="https://github.com/$CMESH_REPO/releases/download/$version/$asset"
  tmpdir="${TMPDIR:-/tmp}/cmesh-install.$$"
  mkdir -p "$tmpdir"
  trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM
  info "downloading $url"
  download "$url" "$tmpdir/$asset" || die "download failed: $url"
  tar -xzf "$tmpdir/$asset" -C "$tmpdir"
  bin="$tmpdir/cmesh"
  if [ "$os" = "windows" ]; then
    bin="$tmpdir/cmesh.exe"
  fi
  if [ ! -f "$bin" ]; then
    if [ -f "$tmpdir/cmesh" ]; then
      bin="$tmpdir/cmesh"
    elif [ -f "$tmpdir/cmesh.exe" ]; then
      bin="$tmpdir/cmesh.exe"
    else
      die "release archive did not contain cmesh binary"
    fi
  fi
  install_file "$bin" "$install_path"
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

install_name=cmesh
if [ "$(detect_os)" = "windows" ]; then
  install_name=cmesh.exe
fi
install_path="$install_dir/$install_name"

if install_from_binary; then
  info "installed $install_path from CMESH_BIN"
elif install_from_source; then
  info "installed $install_path from source"
elif install_from_release; then
  info "installed $install_path from GitHub release"
else
  printf '%s\n' "cmesh install: no install source found." >&2
  printf '%s\n' "  Most likely you ran this from the scripts/ directory. Run it from the repo root:" >&2
  printf '%s\n' "    cd \"\$(dirname \"\$0\")/..\" && sh scripts/install.sh   # or just: cd .. && sh scripts/install.sh" >&2
  printf '%s\n' "  Or provide a source explicitly:" >&2
  printf '%s\n' "    CMESH_BIN=/path/to/cmesh sh scripts/install.sh         # install a prebuilt binary" >&2
  printf '%s\n' "    CMESH_REPO=owner/repo sh scripts/install.sh            # download a published release" >&2
  exit 1
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

# How the user invokes cmesh from here on: bare name if it's on PATH, else the
# full install path (the new PATH entry isn't active in this already-running shell).
cmesh_cmd="cmesh"
if [ "$on_path" != "1" ]; then
  cmesh_cmd="$install_path"
fi

# next_steps prints the full "now what?" guidance. The #1 confusion is that the
# MCP gets wired but the user is left at a cliff, so spell out BOTH remaining
# steps explicitly: register the MCP into the agent(s), then drive setup from
# inside the agent with one phrase.
next_steps() {
  printf '%s\n' ""
  printf '%s\n' "============================================================"
  printf '%s\n' "  What to do next"
  printf '%s\n' "============================================================"
  printf '%s\n' ""
  printf '%s\n' "  1. Register the cmesh MCP into your AI coding agent(s):"
  printf '%s\n' "         $cmesh_cmd install"
  printf '%s\n' ""
  printf '%s\n' "  2. Open a NEW session in that agent and type exactly:"
  printf '%s\n' ""
  printf '%s\n' "         help me setup cmesh"
  printf '%s\n' ""
  printf '%s\n' "     cmesh guides the rest from inside the agent (detects your"
  printf '%s\n' "     tailnet, writes config, starts the service, links a peer,"
  printf '%s\n' "     shows example prompts). Just answer the yes/no prompts."
  printf '%s\n' "============================================================"
}

# Part 1 step 3: auto-launch the native interactive agent picker (Part 2) so the
# user goes straight from one curl|sh into "pick your agent" — unless they opted
# out with --no-agent-setup, or stdin/stdout is not a terminal (piped install,
# where we cannot drive the picker). In the interactive case, `cmesh install`
# prints its own loud "open a new session and say: help me setup cmesh" banner,
# so we don't duplicate it here.
if [ "$no_agent_setup" = "1" ]; then
  info ""
  info "Binary installed. Agent setup skipped (--no-agent-setup)."
  next_steps
elif [ -t 0 ] && [ -t 1 ]; then
  info ""
  info "Launching the cmesh agent picker..."
  info ""
  # Run via the resolved install path so it works before the new PATH entry is
  # active in this shell. cmesh install prints the next-steps banner itself.
  "$install_path" install
elif can_use_dev_tty; then
  info ""
  info "Launching the cmesh agent picker..."
  info ""
  # `curl ... | sh` makes stdin the script body, but the user's terminal is still
  # available at /dev/tty in normal shells. Reattach the picker there so the
  # one-liner can complete the MCP registration too.
  "$install_path" install </dev/tty >/dev/tty
else
  # Non-interactive (piped) install: we can't drive the picker, so tell the user
  # the complete remaining path, not just "run cmesh install".
  info ""
  info "Binary installed. The terminal can't run the interactive picker here."
  next_steps
fi
