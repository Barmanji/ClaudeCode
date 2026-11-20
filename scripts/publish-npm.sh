#!/usr/bin/env bash
# Build and publish the npm packages from an existing GitHub release.
#
#   ./scripts/publish-npm.sh 0.1.0
#
# Requires: gh (authenticated), npm (authenticated via NODE_AUTH_TOKEN or
# `npm login`). Set NPM_PUBLISH_DRY_RUN=1 to assemble without publishing.

set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
	echo "usage: $0 <version>   (e.g. 0.1.0, without the leading v)" >&2
	exit 2
fi
VERSION="${VERSION#v}"

REPO="${CLAWD_REPO:-Barmanji/ClaudeCode}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/npm/dist"

command -v gh >/dev/null || { echo "error: gh is required" >&2; exit 1; }
command -v npm >/dev/null || { echo "error: npm is required" >&2; exit 1; }

rm -rf "$DIST"
mkdir -p "$DIST"

DRY_RUN=()
if [[ -n "${NPM_PUBLISH_DRY_RUN:-}" ]]; then
	DRY_RUN=(--dry-run)
fi

DIST_TAG=latest
if [[ "$VERSION" == *-* ]]; then
	DIST_TAG=next
fi

publish_dir() {
	(cd "$1" && npm publish --access public --tag "$DIST_TAG" "${DRY_RUN[@]}")
}

# goos goarch npm_os npm_cpu
PLATFORMS=(
	"linux amd64 linux x64"
	"linux arm64 linux arm64"
	"darwin amd64 darwin x64"
	"darwin arm64 darwin arm64"
	"windows amd64 win32 x64"
)

for entry in "${PLATFORMS[@]}"; do
	read -r goos goarch npm_os npm_cpu <<<"$entry"
	pkg="c-lawd-code-${npm_os}-${npm_cpu}"
	dir="$DIST/$pkg"
	asset="clawd_${goos}_${goarch}"
	mkdir -p "$dir/bin"

	echo "==> $pkg"
	gh release download "v$VERSION" --repo "$REPO" --pattern "$asset" --dir "$dir"
	if [[ "$goos" == "windows" ]]; then
		mv "$dir/$asset" "$dir/bin/clawd.exe"
		chmod 0755 "$dir/bin/clawd.exe"
	else
		mv "$dir/$asset" "$dir/bin/clawd"
		chmod 0755 "$dir/bin/clawd"
	fi

	cat >"$dir/package.json" <<EOF
{
  "name": "$pkg",
  "version": "$VERSION",
  "description": "Prebuilt binary for c-lawd-code ($goos/$goarch)",
  "homepage": "https://github.com/$REPO",
  "repository": { "type": "git", "url": "git+https://github.com/$REPO.git" },
  "license": "MIT",
  "os": ["$npm_os"],
  "cpu": ["$npm_cpu"],
  "files": ["bin"]
}
EOF

	publish_dir "$dir"
done

echo "==> c-lawd-code (launcher)"
sed "s/__VERSION__/$VERSION/g" "$ROOT/npm/package.json" >"$DIST/package.json"
cp -R "$ROOT/npm/bin" "$ROOT/npm/lib" "$DIST/"
cp "$ROOT/README.md" "$ROOT/LICENSE" "$DIST/"
publish_dir "$DIST"

echo "published c-lawd-code@$VERSION (dist-tag: $DIST_TAG)"
