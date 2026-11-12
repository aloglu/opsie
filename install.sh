#!/bin/sh
# Install opsie, a dashboard for keeping an eye on your server.
#
#   curl -fsSL https://raw.githubusercontent.com/aloglu/opsie/main/install.sh | sh
#
# This downloads the prebuilt binary for your system from the latest GitHub
# release, checks it against the release's checksums, and puts it in
# ~/.local/bin. It doesn't need sudo or Go, and it doesn't change your
# shell config.
#
# Settings (environment variables):
#   OPSIE_INSTALL_DIR  where to put opsie (default: ~/.local/bin)
#   OPSIE_VERSION      a release tag such as v0.3.1 (default: latest)

set -eu

REPO="aloglu/opsie"
INSTALL_DIR="${OPSIE_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${OPSIE_VERSION:-latest}"
BASE="${OPSIE_DOWNLOAD_BASE:-https://github.com/$REPO/releases}"

if [ -t 1 ]; then
	bold=$(printf '\033[1m') dim=$(printf '\033[2m') red=$(printf '\033[31m')
	green=$(printf '\033[32m') yellow=$(printf '\033[33m') reset=$(printf '\033[0m')
else
	bold='' dim='' red='' green='' yellow='' reset=''
fi

say() { printf '%s\n' "$*"; }
fail() {
	printf '%s✗ %s%s\n' "$red" "$*" "$reset" >&2
	exit 1
}

[ "$(uname -s)" = Linux ] || fail "opsie runs on Linux servers; this system is $(uname -s)."
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "there's no prebuilt opsie for $(uname -m) processors." ;;
esac

if command -v curl >/dev/null 2>&1; then
	download() { curl -fsSL --retry 2 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	download() { wget -q -O "$2" "$1"; }
else
	fail "installing needs curl or wget."
fi

command -v sha256sum >/dev/null 2>&1 || fail "installing needs sha256sum to verify the download."

asset="opsie_linux_${arch}.tar.gz"
if [ "$VERSION" = latest ]; then
	url="$BASE/latest/download"
else
	url="$BASE/download/$VERSION"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say ""
say "  ${bold}Installing opsie${reset} ${dim}for linux/$arch…${reset}"

download "$url/checksums.txt" "$tmp/checksums.txt" ||
	fail "couldn't download the release from $url. Check your connection and try again."
download "$url/$asset" "$tmp/$asset" ||
	fail "couldn't download $asset."

expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "the release has no checksum for $asset."
[ "$(sha256sum "$tmp/$asset" | cut -d ' ' -f 1)" = "$expected" ] ||
	fail "the download doesn't match its checksum, so it wasn't installed. Please try again."

tar -xzf "$tmp/$asset" -C "$tmp" opsie || fail "couldn't unpack $asset."

mkdir -p "$INSTALL_DIR" || fail "couldn't create $INSTALL_DIR."
# Copy next to the destination, then rename, so an existing opsie is
# replaced in one step.
cp "$tmp/opsie" "$INSTALL_DIR/.opsie.new" || fail "couldn't write to $INSTALL_DIR."
chmod 755 "$INSTALL_DIR/.opsie.new"
mv -f "$INSTALL_DIR/.opsie.new" "$INSTALL_DIR/opsie"

# The new opsie prints the welcome; fall back to plain text if it can't.
if ! "$INSTALL_DIR/opsie" installed 2>/dev/null; then
	say "  ${green}✓${reset} opsie is installed in $INSTALL_DIR. Run ${bold}opsie${reset} to open it."
fi

# Another copy earlier on PATH would shadow this one.
found=$(command -v opsie 2>/dev/null || true)
if [ -n "$found" ] && [ "$found" != "$INSTALL_DIR/opsie" ]; then
	say "  ${yellow}Note:${reset} there's another opsie at $found, which your shell runs instead."
	say "  ${dim}Remove it with:${reset} rm \"$found\""
	say ""
fi
