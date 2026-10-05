#!/bin/sh
# Compile every sample document so the Tectonic support files and format, and
# the Typst packages Markdown rendering imports, land in the compiler caches.
# The caches are whatever TECTONIC_CACHE_DIR and XDG_CACHE_HOME point at.
set -eu

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$(dirname "$0")"/* "$work"
cd "$work"

for document in article.tex report.tex slides.tex; do
	echo "prewarm: $document"
	tectonic --chatter minimal "$document"
done

echo "prewarm: markdown.typ"
typst compile markdown.typ
