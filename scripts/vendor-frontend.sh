#!/bin/sh
# Fetch the pinned frontend libraries the editor needs and vendor them into
# internal/app/static/vendor, so the UI never loads code from a third-party CDN
# and works on networks with no internet access.
#
# Each tarball is checked against the sha512 integrity hash the npm registry
# published for that exact version. Update a library by changing its version and
# integrity below, then rerun: make vendor
set -eu

MONACO_VERSION=0.45.0
MONACO_INTEGRITY=sha512-mjv1G1ZzfEE3k9HZN0dQ2olMdwIfaeAAjFiwNprLfYNRSz7ctv9XuCT7gPtBGrMUeV1/iZzYKj17Khu1hxoHOA==

PDFJS_VERSION=3.11.174
PDFJS_INTEGRITY=sha512-TdTZPf1trZ8/UFu5Cx/GXB7GZM30LT+wWUNfsi6Bq8ePLnb+woNKtDymI2mxZYBpMbonNFqKmiz684DIfnd8dA==

ROOT=$(cd "$(dirname "$0")/.." && pwd)
VENDOR="$ROOT/internal/app/static/vendor"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fetch() {
	name=$1 version=$2 integrity=$3
	tarball="$WORK/$name-$version.tgz"
	curl -fsSL "https://registry.npmjs.org/$name/-/$name-$version.tgz" -o "$tarball"
	actual="sha512-$(openssl dgst -sha512 -binary "$tarball" | base64 | tr -d '\n')"
	if [ "$actual" != "$integrity" ]; then
		echo "integrity mismatch for $name@$version" >&2
		echo "  expected $integrity" >&2
		echo "  actual   $actual" >&2
		exit 1
	fi
	mkdir -p "$WORK/$name"
	tar -xzf "$tarball" -C "$WORK/$name"
}

fetch monaco-editor "$MONACO_VERSION" "$MONACO_INTEGRITY"
fetch pdfjs-dist "$PDFJS_VERSION" "$PDFJS_INTEGRITY"

# Versioned directories let the server cache these files as immutable.
MONACO_DIR="$VENDOR/monaco-editor-$MONACO_VERSION"
PDFJS_DIR="$VENDOR/pdfjs-dist-$PDFJS_VERSION"
rm -rf "$VENDOR"
mkdir -p "$MONACO_DIR" "$PDFJS_DIR"

cp -R "$WORK/monaco-editor/package/min/vs" "$MONACO_DIR/vs"
# The UI is English only, so the translated message bundles are never loaded.
rm -f "$MONACO_DIR"/vs/editor/editor.main.nls.*.js
cp "$WORK/monaco-editor/package/LICENSE" "$MONACO_DIR/LICENSE"

cp "$WORK/pdfjs-dist/package/build/pdf.min.js" "$PDFJS_DIR/pdf.min.js"
cp "$WORK/pdfjs-dist/package/build/pdf.worker.min.js" "$PDFJS_DIR/pdf.worker.min.js"
cp "$WORK/pdfjs-dist/package/LICENSE" "$PDFJS_DIR/LICENSE"

echo "Vendored monaco-editor $MONACO_VERSION and pdfjs-dist $PDFJS_VERSION into $VENDOR"
