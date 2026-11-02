#!/bin/sh
# Verifies the release install path: a matching checksums.txt installs, a
# mismatched one refuses to install, and a release without checksums.txt still
# installs (the documented `curl | sh` path and older releases must not break).
#
# Hermetic: fake curl/tar on PATH and a copied install.sh in a temp dir, so no
# network, no Go toolchain and no source checkout are used.
set -eu

script_dir=$(dirname -- "$0")
repo_root=$(cd "$script_dir/.." && pwd) || exit 1
work=$(mktemp -d "${TMPDIR:-/tmp}/cmesh-install-check.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM

fail() {
  printf '%s\n' "install-integrity: $*" >&2
  exit 1
}

detect_asset() {
  os=$(uname -s)
  case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "unsupported test host: $os" ;;
  esac
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) fail "unsupported test arch: $arch" ;;
  esac
  printf 'cmesh_%s_%s.tar.gz' "$os" "$arch"
}

asset=$(detect_asset)
fakebin=$work/bin
mkdir -p "$fakebin" "$work/run" "$work/ok" "$work/bad" "$work/nosums"
cp "$repo_root/scripts/install.sh" "$work/install.sh"

cat > "$fakebin/curl" <<'EOF'
#!/bin/sh
url=''
out=''
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
[ -n "$url" ] && [ -n "$out" ] || exit 22
case "$url" in
  */checksums.txt)
    [ -n "${FAKE_SUM:-}" ] || exit 22
    printf '%s  ./%s\n' "$FAKE_SUM" "$FAKE_ASSET" > "$out"
    ;;
  *"$FAKE_ASSET")
    printf 'ARCHIVE-CONTENT' > "$out"
    ;;
  *) exit 22 ;;
esac
EOF
chmod 0755 "$fakebin/curl"

cat > "$fakebin/tar" <<'EOF'
#!/bin/sh
dir=''
while [ $# -gt 0 ]; do
  case "$1" in
    -C) dir=$2; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$dir" ] || exit 1
printf '#!/bin/sh\nexit 0\n' > "$dir/cmesh"
chmod 0755 "$dir/cmesh"
EOF
chmod 0755 "$fakebin/tar"

if command -v sha256sum >/dev/null 2>&1; then
  good_sum=$(printf 'ARCHIVE-CONTENT' | sha256sum | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  good_sum=$(printf 'ARCHIVE-CONTENT' | shasum -a 256 | awk '{print $1}')
else
  fail "no SHA-256 tool available"
fi

run_install() {
  dest=$1
  sum=$2
  ( cd "$work" && FAKE_SUM=$sum FAKE_ASSET=$asset \
    PATH="$fakebin:$PATH" TMPDIR="$work/run" \
    CMESH_VERSION=v9.9.9 CMESH_REPO=example/cmesh CMESH_INSTALL_DIR="$dest" \
    sh "$work/install.sh" --no-agent-setup >/dev/null 2>&1 )
}

# 1. A matching checksum installs.
run_install "$work/ok" "$good_sum" || fail "matching checksum did not install"
[ -x "$work/ok/cmesh" ] || fail "matching checksum did not install a binary"

# 2. A mismatch refuses to install.
if run_install "$work/bad" "0000000000000000000000000000000000000000000000000000000000000000"; then
  fail "installer accepted a mismatched checksum"
fi
[ ! -e "$work/bad/cmesh" ] || fail "installer installed a binary after a checksum mismatch"

# 3. A release without checksums.txt still installs (warn and continue).
run_install "$work/nosums" "" || fail "release without checksums.txt must still install"
[ -x "$work/nosums/cmesh" ] || fail "release without checksums.txt did not install a binary"

printf '%s\n' "install-integrity: ok"
