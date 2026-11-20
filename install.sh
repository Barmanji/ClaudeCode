#!/bin/sh
# C-Lawd Code installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/Barmanji/ClaudeCode/main/install.sh | sh
#
# Environment:
#   CLAWD_VERSION       install a specific version (e.g. 0.1.0)
#   CLAWD_INSTALL_DIR   install directory (default: ~/.local/bin)
#   CLAWD_REPO          owner/repo to download from

set -eu

REPO="${CLAWD_REPO:-Barmanji/ClaudeCode}"
PROJECT="c-lawd-code"
BIN="clawd"

info() { printf '  %s\n' "$*" >&2; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

need uname
need tar
if command -v curl >/dev/null 2>&1; then
	download() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	download() { wget -qO "$2" "$1"; }
else
	fail "missing curl or wget"
fi

sha256_check() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c "$1" >/dev/null
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$1" >/dev/null
	else
		fail "missing sha256sum or shasum; refusing to install an unverified binary"
	fi
}

os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported OS: $os (on Windows use: npm install -g c-lawd-code)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture: $arch" ;;
esac

version="${CLAWD_VERSION:-}"
if [ -z "$version" ]; then
	latest_url="https://api.github.com/repos/$REPO/releases/latest"
	if command -v curl >/dev/null 2>&1; then
		tag=$(curl -fsSL "$latest_url" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)
	else
		tag=$(wget -qO- "$latest_url" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)
	fi
	[ -n "$tag" ] || fail "could not determine the latest release (set CLAWD_VERSION manually)"
	version="$tag"
fi
version="${version#v}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

asset="${PROJECT}_${version}_${os}_${arch}.tar.gz"
base="${CLAWD_RELEASE_BASE:-https://github.com/$REPO/releases/download/v${version}}"

info "downloading $asset"
download "$base/$asset" "$tmp/$asset" || fail "download failed: $base/$asset"
download "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download checksums.txt"

grep " $asset\$" "$tmp/checksums.txt" >"$tmp/verify.txt" || fail "no checksum for $asset"
(cd "$tmp" && sha256_check verify.txt) || fail "checksum verification failed"

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/$BIN" ] || fail "archive did not contain $BIN"
chmod 0755 "$tmp/$BIN"

if [ -n "${CLAWD_INSTALL_DIR:-}" ]; then
	install_dir="$CLAWD_INSTALL_DIR"
elif [ -d "$HOME/.local/bin" ] || mkdir -p "$HOME/.local/bin" 2>/dev/null; then
	install_dir="$HOME/.local/bin"
elif [ -w /usr/local/bin ]; then
	install_dir="/usr/local/bin"
else
	install_dir="/usr/local/bin"
fi

mkdir -p "$install_dir" 2>/dev/null || fail "cannot create $install_dir (set CLAWD_INSTALL_DIR)"
mv "$tmp/$BIN" "$install_dir/$BIN" 2>/dev/null || fail "cannot write to $install_dir; retry with sudo or set CLAWD_INSTALL_DIR"

printf '\n%s %s installed to %s\n\n' "$PROJECT" "$version" "$install_dir/$BIN" >&2
case ":$PATH:" in
	*":$install_dir:"*) ;;
	*) info "add this to your shell profile to use the command:"; info "  export PATH=\"$install_dir:\$PATH\"" ;;
esac
info "next: run 'clawd setup'"
