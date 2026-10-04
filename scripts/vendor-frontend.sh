#!/bin/sh
# Fetch the pinned frontend libraries the editor needs into
# internal/app/static/vendor, where go:embed bundles them into the binary, so
# the UI never loads code from a third-party CDN. The directory is not committed:
# make dev/build/test and the Docker build run this script.
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

# Only the files the editor loads. Its LaTeX, BibTeX, and Typst modes are
# defined in editor.js, so Markdown is the one Monaco language it uses, and the
# UI is English only, so no translated message bundles.
for file in \
	vs/loader.js \
	vs/editor/editor.main.js \
	vs/editor/editor.main.css \
	vs/editor/editor.main.nls.js \
	vs/base/worker/workerMain.js \
	vs/base/common/worker/simpleWorker.nls.js \
	vs/base/browser/ui/codicons/codicon/codicon.ttf \
	vs/basic-languages/markdown/markdown.js; do
	mkdir -p "$MONACO_DIR/$(dirname "$file")"
	cp "$WORK/monaco-editor/package/min/$file" "$MONACO_DIR/$file"
done
cp "$WORK/monaco-editor/package/LICENSE" "$MONACO_DIR/LICENSE"

cp "$WORK/pdfjs-dist/package/build/pdf.min.js" "$PDFJS_DIR/pdf.min.js"
cp "$WORK/pdfjs-dist/package/build/pdf.worker.min.js" "$PDFJS_DIR/pdf.worker.min.js"
cp "$WORK/pdfjs-dist/package/LICENSE" "$PDFJS_DIR/LICENSE"

echo "Vendored monaco-editor $MONACO_VERSION and pdfjs-dist $PDFJS_VERSION into $VENDOR"
