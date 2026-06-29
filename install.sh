#!/bin/sh
# recoil installer — downloads the latest prebuilt release for your OS/arch.
#
#   curl -sSfL https://raw.githubusercontent.com/EclipseElips/recoil/main/install.sh | sh
#   ... | sh -s -- -b /usr/local/bin        # choose install dir (default ./bin)
#   ... | sh -s -- -b ~/.local/bin v1.0.0   # pin a version
#
# No Go toolchain required. For a source build use `go install` instead.
set -eu

REPO="EclipseElips/recoil"
BINDIR="./bin"
VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    -b) BINDIR="${2:?-b needs a directory}"; shift 2 ;;
    v[0-9]*|[0-9]*) VERSION="$1"; shift ;;
    *) echo "recoil install: unknown argument: $1" >&2; exit 1 ;;
  esac
done

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) echo "recoil install: unsupported OS '$os' — try: go install $REPO@latest" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "recoil install: unsupported arch '$arch' — try: go install $REPO@latest" >&2; exit 1 ;;
esac

if [ -z "$VERSION" ]; then
  VERSION=$(curl -sSfL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' | head -n1 | cut -d'"' -f4)
fi
[ -n "$VERSION" ] || { echo "recoil install: could not find the latest release" >&2; exit 1; }

archive="recoil_${VERSION#v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "recoil install: downloading $archive ($VERSION)"
curl -sSfL "$base/$archive" -o "$tmp/$archive"

# Verify the checksum if the release publishes one.
if command -v sha256sum >/dev/null 2>&1; then sha="sha256sum"
elif command -v shasum  >/dev/null 2>&1; then sha="shasum -a 256"
else sha=""; fi
if [ -n "$sha" ] && curl -sSfL "$base/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
  if ( cd "$tmp" && grep " $archive\$" checksums.txt | $sha -c - >/dev/null 2>&1 ); then
    echo "recoil install: checksum ok"
  else
    echo "recoil install: warning: checksum did not verify" >&2
  fi
fi

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$BINDIR"
cp "$tmp/recoil" "$BINDIR/recoil"
chmod 0755 "$BINDIR/recoil"

echo "recoil install: installed $BINDIR/recoil"
case ":$PATH:" in
  *":$BINDIR:"*) ;;
  *) echo "recoil install: add $BINDIR to your PATH to run 'recoil' directly" ;;
esac
